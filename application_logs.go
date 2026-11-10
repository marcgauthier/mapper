package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/marcgauthier/loggy"
)

const userLogCategory = "User action"
const applicationLogCategory = "Application logs"

var credentialAssignment = regexp.MustCompile(`(?i)\b(password|token|secret|private_key|database_key)\s*[:=]\s*[^\s,;]+`)

func safeLogMessage(message string) string {
	return credentialAssignment.ReplaceAllString(message, "$1=[redacted]")
}

func (a *App) writeStructuredLog(entry loggy.Entry) {
	db := a.actionLogs()
	if db == nil {
		return
	}
	if err := db.Write(entry); err != nil {
		fmt.Fprintf(os.Stderr, "Mapper Loggy write failed: %v\n", err)
	}
}
func (a *App) applicationLog(level, message string, fields map[string]string) {
	values := map[string]string{"service": "mapper", "category": applicationLogCategory, "level": level}
	for key, value := range fields {
		values[key] = safeLogMessage(value)
	}
	a.writeStructuredLog(loggy.Entry{Time: time.Now(), Message: safeLogMessage(message), Fields: values})
}

type applicationLogWriter struct {
	app   *App
	level string
}

func (w applicationLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if line != "" {
			w.app.applicationLog(w.level, line, nil)
		}
	}
	return len(p), nil
}
func (a *App) captureStandardLogs() func() {
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(os.Stderr, applicationLogWriter{a, "info"}))
	return func() { log.SetOutput(previous) }
}

type requestLogKey struct{}
type requestLogState struct{ requestID, message, stack string }
type requestLogWriter struct {
	http.ResponseWriter
	status  int
	size    int64
	state   *requestLogState
	capture bool
}

func (w *requestLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *requestLogWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *requestLogWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.capture && w.status >= 400 && len(w.state.message) < 4096 {
		remain := 4096 - len(w.state.message)
		w.state.message += string(p[:min(len(p), remain)])
	}
	n, err := w.ResponseWriter.Write(p)
	w.size += int64(n)
	return n, err
}
func (w *requestLogWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *requestLogWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = 101
	}
	return c, b, err
}
func (w *requestLogWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.CopyBuffer(struct{ io.Writer }{w}, r, make([]byte, 32<<10))
}

func captureHTTPFailures(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, _ := r.Context().Value(requestLogKey{}).(*requestLogState)
		if state == nil {
			state = &requestLogState{}
		}
		out := &requestLogWriter{ResponseWriter: w, state: state, capture: true}
		defer func() {
			if recovered := recover(); recovered != nil {
				state.message = fmt.Sprint(recovered)
				state.stack = string(debug.Stack())
				if out.status == 0 {
					http.Error(out, "internal server error", 500)
				}
			}
		}()
		next.ServeHTTP(out, r)
	})
}

func (a *App) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		state := &requestLogState{requestID: newID()}
		r = r.WithContext(context.WithValue(r.Context(), requestLogKey{}, state))
		out := &requestLogWriter{ResponseWriter: w, state: state}
		out.Header().Set("X-Request-ID", state.requestID)
		actor := "anonymous"
		if p, ok := a.identity(r); ok {
			actor = p.User
		}
		encoding := r.Header.Get("Content-Encoding")
		defer func() {
			if recovered := recover(); recovered != nil {
				state.message = fmt.Sprint(recovered)
				state.stack = string(debug.Stack())
				if out.status == 0 {
					out.WriteHeader(500)
				}
			}
			status := out.status
			if status == 0 {
				status = 200
			}
			level := "info"
			if status >= 500 || state.stack != "" {
				level = "error"
			} else if status >= 400 {
				level = "warn"
			}
			fields := map[string]string{"request_id": state.requestID, "method": r.Method, "path": r.URL.Path, "status": strconv.Itoa(status), "duration_ms": strconv.FormatInt(time.Since(start).Milliseconds(), 10), "response_bytes": strconv.FormatInt(out.size, 10), "actor": actor, "request_encoding": encoding}
			if state.message != "" {
				fields["error"] = strings.TrimSpace(state.message)
			}
			if state.stack != "" {
				fields["stack"] = state.stack
			}
			a.applicationLog(level, "HTTP "+r.Method+" "+r.URL.Path, fields)
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && r.URL.Path != "/api/client-errors" {
				delete(fields, "stack")
				delete(fields, "error")
				fields["service"] = "mapper"
				fields["category"] = userLogCategory
				fields["level"] = level
				fields["action"] = "http_request"
				fields["outcome"] = "success"
				if status >= 400 {
					fields["outcome"] = "failed"
				}
				a.writeStructuredLog(loggy.Entry{Time: time.Now(), Message: actor + " " + r.Method + " " + r.URL.Path, Fields: fields})
			}
		}()
		next.ServeHTTP(out, r)
	})
}

// Build safe human-readable snapshots without retaining credentials from object
// metadata or account updates in a second log store.
func redactLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lower := strings.ToLower(key)
			if lower == "key" || lower == "cookie" || lower == "authorization" || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "private_key") {
				typed[key] = "[redacted]"
			} else {
				typed[key] = redactLogValue(item)
			}
		}
	case []any:
		for i, item := range typed {
			typed[i] = redactLogValue(item)
		}
	}
	return value
}

// Immutable queued entries are written only after their SQL transaction commits.
func (a *App) commitLogged(concrete *sql.Tx) error {
	a.auditLogMu.Lock()
	entries := a.pendingAudit[concrete]
	delete(a.pendingAudit, concrete)
	a.auditLogMu.Unlock()
	if err := concrete.Commit(); err != nil {
		a.applicationLog("error", "Database transaction commit failed", map[string]string{"error": err.Error()})
		return err
	}
	for _, entry := range entries {
		a.writeStructuredLog(entry)
	}
	return nil
}
func (a *App) rollbackLogged(tx *sql.Tx) error {
	a.auditLogMu.Lock()
	delete(a.pendingAudit, tx)
	a.auditLogMu.Unlock()
	return tx.Rollback()
}

// Keep the HTTP recorder independent of response payload buffering.
var _ io.ReaderFrom = (*requestLogWriter)(nil)

// Client diagnostics are authenticated, bounded and stored only as system logs.
func (a *App) clientError(w http.ResponseWriter, r *http.Request, p principal) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var event struct {
		Message string `json:"message"`
		Stack   string `json:"stack"`
		Source  string `json:"source"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8193)).Decode(&event); err != nil || len(event.Message)+len(event.Stack)+len(event.Source) > 8192 {
		http.Error(w, "invalid client diagnostic", 400)
		return
	}
	source := ""
	if parsed, err := url.Parse(event.Source); err == nil {
		source = parsed.Path
	}
	a.applicationLog("error", "Browser: "+safeLogMessage(event.Message), map[string]string{"actor": p.User, "source": source, "stack": event.Stack})
	w.WriteHeader(http.StatusNoContent)
}
