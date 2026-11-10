package app

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Export one consistent snapshot, in dependency order for transactional import.
// metadata_json preserves arbitrary metadata and arrays without lossy flattening.
func (a *App) exportCSV(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer tx.Rollback()
	header := []string{"kind", "id", "name", "x", "y", "device_id", "source_interface", "target_interface", "panel_id", "port_a", "port_b", "link_id", "infrastructure_id", "sequence", "metadata_json"}
	var records [][]string
	add := func(values map[string]string) {
		row := make([]string, len(header))
		for i, key := range header {
			row[i] = values[key]
		}
		records = append(records, row)
	}
	queries := []struct {
		sql     string
		columns []string
		kind    string
	}{
		{`SELECT kind,id,name,x,y,data FROM objects ORDER BY CASE kind WHEN 'site' THEN 0 WHEN 'building' THEN 1 WHEN 'floor' THEN 2 WHEN 'room' THEN 3 WHEN 'telecom_room' THEN 3 WHEN 'rack' THEN 4 WHEN 'device' THEN 5 WHEN 'virtual_machine' THEN 7 WHEN 'virtual_switch' THEN 7 ELSE 6 END,id`, []string{"kind", "id", "name", "x", "y", "metadata_json"}, ""},
		{`SELECT id,device_id,name,data FROM interfaces ORDER BY id`, []string{"id", "device_id", "name", "metadata_json"}, "interface"},
		{`SELECT id,source_interface,target_interface,data FROM links ORDER BY id`, []string{"id", "source_interface", "target_interface", "metadata_json"}, "connection"},
		{`SELECT id,panel_id,port_a,port_b FROM patch_pairings ORDER BY id`, []string{"id", "panel_id", "port_a", "port_b"}, "pairing"},
		{`SELECT id,link_id,infrastructure_id,sequence FROM cable_routes ORDER BY link_id,sequence`, []string{"id", "link_id", "infrastructure_id", "sequence"}, "route"},
	}
	for _, query := range queries {
		rows, e := tx.Query(query.sql)
		if e != nil {
			http.Error(w, "export lookup failed", 500)
			return
		}
		for rows.Next() {
			values := make([]any, len(query.columns))
			targets := make([]any, len(values))
			for i := range values {
				targets[i] = &values[i]
			}
			if e = rows.Scan(targets...); e != nil {
				break
			}
			record := map[string]string{"kind": query.kind}
			for i, key := range query.columns {
				switch value := values[i].(type) {
				case []byte:
					record[key] = string(value)
				case float64:
					record[key] = strconv.FormatFloat(value, 'f', -1, 64)
				case nil:
					record[key] = ""
				default:
					record[key] = fmt.Sprint(value)
				}
			}
			if raw := record["metadata_json"]; raw != "" && !json.Valid([]byte(raw)) {
				e = fmt.Errorf("invalid metadata")
				break
			}
			add(record)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "export read failed", 500)
			return
		}
	}

	var relationshipsExist int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='relationships'`).Scan(&relationshipsExist); err != nil {
		http.Error(w, "relationship schema lookup failed", 500)
		return
	}
	if relationshipsExist > 0 {
		rows, e := tx.Query(`SELECT id,source_id,target_id,relationship_type,version,label FROM relationships ORDER BY CASE relationship_type WHEN 'hosts' THEN 0 ELSE 1 END,id`)
		if e != nil {
			http.Error(w, "relationship export failed", 500)
			return
		}
		for rows.Next() {
			var rel topologyRelationship
			if e = rows.Scan(&rel.ID, &rel.Source, &rel.Target, &rel.Type, &rel.Version, &rel.Label); e != nil {
				break
			}
			raw, _ := json.Marshal(rel)
			add(map[string]string{"kind": "relationship", "id": rel.ID, "metadata_json": string(raw)})
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "relationship export failed", 500)
			return
		}
	}

	// Preserve archived geographic metadata from older releases in exports.
	var exists int
	if e := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='geography'`).Scan(&exists); e != nil {
		http.Error(w, "geography schema lookup failed", 500)
		return
	}
	if exists > 0 {
		rows, e := tx.Query(`SELECT g.owner_type,g.owner_id,g.version,g.data FROM geography g LEFT JOIN objects o ON o.id=g.owner_id AND g.owner_type='object' ORDER BY CASE WHEN g.owner_type='link' THEN 9 WHEN o.kind='building' THEN 0 WHEN o.kind='telecom_room' THEN 1 WHEN o.kind='rack' THEN 2 ELSE 3 END,g.owner_id`)
		if e != nil {
			http.Error(w, "geography export failed", 500)
			return
		}
		for rows.Next() {
			var g archivedGeographyRecord
			var raw string
			if e = rows.Scan(&g.Type, &g.ID, &g.Version, &raw); e != nil {
				break
			}
			if e = json.Unmarshal([]byte(raw), &g.Data); e != nil {
				break
			}
			encoded, _ := json.Marshal(g)
			add(map[string]string{"kind": "geography", "id": g.ID, "metadata_json": string(encoded)})
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "geography export read failed", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "export snapshot failed", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mapper-topology.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	writer := csv.NewWriter(w)
	_ = writer.Write(header)
	_ = writer.WriteAll(records)
	writer.Flush()
}
