package main

// Graph actions are audited in SQL for undo and queued for Loggy after the
// transaction commits. User activity and application diagnostics have separate
// categories and role permissions. Loggy retention is configurable.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marcgauthier/loggy"
)

const maxLogDetailBytes = 8192

func openActionLogs(dir string, retentionDays int) (*loggy.DB, error) {
	return loggy.Open(dir,
		loggy.WithRetentionDays(retentionDays),
		loggy.WithStreamFields("service", "category", "level"),
		loggy.WithFlushInterval(2*time.Second),
	)
}

func (a *App) actionLogs() *loggy.DB {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.logs
}

func (a *App) setActionLogs(db *loggy.DB) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logs = db
}

func (a *App) recordAudit(tx *sql.Tx, actor, action, objectID string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO audit(actor,action,object_id,data,created) VALUES(?,?,?,?,?)`, actor, action, objectID, string(b), time.Now().Unix())
	if err != nil {
		return err
	}
	entry := makeActionLog(actor, action, objectID, b)
	a.auditLogMu.Lock()
	if a.pendingAudit == nil {
		a.pendingAudit = make(map[*sql.Tx][]loggy.Entry)
	}
	a.pendingAudit[tx] = append(a.pendingAudit[tx], entry)
	a.auditLogMu.Unlock()
	return nil
}

func makeActionLog(actor, action, objectID string, detail []byte) loggy.Entry {
	var snapshot any
	if json.Unmarshal(detail, &snapshot) == nil {
		detail, _ = json.Marshal(redactLogValue(snapshot))
	}
	if len(detail) > maxLogDetailBytes {
		detail = detail[:maxLogDetailBytes]
	}
	return loggy.Entry{Time: time.Now(), Message: actor + " " + action + " " + objectID, Fields: map[string]string{"service": "mapper", "category": userLogCategory, "level": "info", "actor": actor, "action": action, "object_id": objectID, "detail": string(detail), "outcome": "committed"}}
}
func (a *App) logAction(actor, action, objectID string, detail []byte) {
	a.writeStructuredLog(makeActionLog(actor, action, objectID, detail))
}

// quoteLogsQL renders a double-quoted LogsQL string literal.
func quoteLogsQL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// buildLogsQuery combines the structured filters into one LogsQL query.
// Clauses are ANDed; results always come back newest first.
func buildLogsQuery(actor, action, text string) string {
	var parts []string
	if actor != "" {
		parts = append(parts, `actor:`+quoteLogsQL(actor))
	}
	if action != "" {
		parts = append(parts, `action:`+quoteLogsQL(action))
	}
	if text = strings.TrimSpace(text); text != "" {
		if len(text) > 200 {
			text = text[:200]
		}
		parts = append(parts, quoteLogsQL(text))
	}
	if len(parts) == 0 {
		return "* | sort by (_time) desc"
	}
	return strings.Join(parts, " ") + " | sort by (_time) desc"
}

func (a *App) queryLogs(w http.ResponseWriter, r *http.Request) {
	db := a.actionLogs()
	if db == nil {
		http.Error(w, "action logs unavailable", 503)
		return
	}
	q := r.URL.Query()
	limit := 200
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			http.Error(w, "limit must be 1..1000", 400)
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	category := q.Get("category")
	if category == "" {
		category = "user"
	}
	var clause string
	switch category {
	case "user":
		clause = `(category:"User action" OR category:="")`
	case "system":
		clause = `category:"Application logs"`
	default:
		http.Error(w, "category must be user or system", 400)
		return
	}
	query := buildLogsQuery(q.Get("actor"), q.Get("action"), q.Get("q"))
	if level := q.Get("level"); level != "" {
		if level != "info" && level != "warn" && level != "error" && level != "debug" {
			http.Error(w, "invalid level", 400)
			return
		}
		clause += " level:" + quoteLogsQL(level)
	}
	rows, err := db.Search(ctx, clause+" "+query, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("log query failed: %v", err), 400)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		fields := map[string]string{}
		for k, v := range e.Fields {
			if k == "_stream" || k == "_stream_id" {
				continue
			}
			fields[k] = v
		}
		out = append(out, map[string]any{"time": e.Time.UTC().Format(time.RFC3339), "message": e.Message, "fields": fields})
	}
	json.NewEncoder(w).Encode(out)
}

func (a *App) logsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/logs" {
		http.NotFound(w, r)
		return
	}
	if a.ready() == nil {
		http.Redirect(w, r, "/unlock", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "web/logs.html")
}
