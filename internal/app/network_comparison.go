package app

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type reviewEntity struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind"`
	Name  string         `json:"name"`
	Owner string         `json:"owner,omitempty"`
	Data  map[string]any `json:"data"`
	X     float64        `json:"x"`
	Y     float64        `json:"y"`
}
type reviewSnapshot struct {
	Format   string         `json:"format"`
	Sequence int64          `json:"sequence"`
	Network  string         `json:"network"`
	Entities []reviewEntity `json:"entities"`
}
type reviewChange struct {
	Type   string        `json:"type"`
	Before *reviewEntity `json:"before,omitempty"`
	After  *reviewEntity `json:"after,omitempty"`
}

func networkIncludes(value any, name string) bool {
	if name == "" {
		return true
	}
	switch v := value.(type) {
	case string:
		for _, n := range strings.Split(v, ",") {
			if strings.TrimSpace(n) == name {
				return true
			}
		}
	case []any:
		for _, n := range v {
			if n == name {
				return true
			}
		}
	}
	return false
}
func captureReview(tx *sql.Tx, network string) (reviewSnapshot, error) {
	out := reviewSnapshot{Format: "mapper.topology.v1", Network: network, Entities: []reviewEntity{}}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&out.Sequence); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT id,kind,name,data,x,y FROM objects ORDER BY id`)
	if err != nil {
		return out, err
	}
	objects := map[string]bool{}
	for rows.Next() {
		var e reviewEntity
		var raw string
		if err = rows.Scan(&e.ID, &e.Kind, &e.Name, &raw, &e.X, &e.Y); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &e.Data); err != nil {
			break
		}
		if networkIncludes(e.Data["network"], network) {
			out.Entities = append(out.Entities, e)
			objects[e.ID] = true
		}
	}
	re := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if re != nil {
		return out, re
	}
	rows, err = tx.Query(`SELECT id,device_id,name,data FROM interfaces ORDER BY id`)
	if err != nil {
		return out, err
	}
	ports := map[string]bool{}
	for rows.Next() {
		var e reviewEntity
		var raw string
		e.Kind = "interface"
		if err = rows.Scan(&e.ID, &e.Owner, &e.Name, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &e.Data); err != nil {
			break
		}
		if objects[e.Owner] {
			ports[e.ID] = true
			out.Entities = append(out.Entities, e)
		}
	}
	re = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if re != nil {
		return out, re
	}
	capturedLinks := map[string]bool{}
	rows, err = tx.Query(`SELECT id,source_interface,target_interface,data FROM links ORDER BY id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e reviewEntity
		var source, target, raw string
		e.Kind = "connection"
		if err = rows.Scan(&e.ID, &source, &target, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &e.Data); err != nil {
			break
		}
		if ports[source] || ports[target] {
			capturedLinks[e.ID] = true
			if e.Data == nil {
				e.Data = map[string]any{}
			}
			e.Data["source_interface"] = source
			e.Data["target_interface"] = target
			out.Entities = append(out.Entities, e)
		}
	}
	re = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if re != nil {
		return out, re
	}
	for _, spec := range []struct {
		kind, query string
		fields      []string
	}{
		{"layout", `SELECT grouping,object_id,x,y FROM layouts ORDER BY grouping,object_id`, nil},
		{"pairing", `SELECT id,panel_id,port_a,port_b FROM patch_pairings ORDER BY id`, []string{"panel_id", "port_a", "port_b"}},
		{"route", `SELECT id,link_id,infrastructure_id,CAST(sequence AS TEXT) FROM cable_routes ORDER BY id`, []string{"link_id", "infrastructure_id", "sequence"}},
		{"relationship", `SELECT id,source_id,target_id,relationship_type,label FROM relationships ORDER BY id`, []string{"source_id", "target_id", "relationship_type", "label"}},
	} {
		rows, err = tx.Query(spec.query)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			e := reviewEntity{Kind: spec.kind, Data: map[string]any{}}
			include := network == ""
			if spec.kind == "layout" {
				var group, object string
				if err = rows.Scan(&group, &object, &e.X, &e.Y); err != nil {
					break
				}
				key, _ := json.Marshal([]string{group, object})
				e.ID = string(key)
				e.Owner = object
				e.Data["grouping"] = group
				e.Name = group
				include = include || objects[object]
			} else {
				values := make([]string, len(spec.fields))
				args := []any{&e.ID}
				for i := range values {
					args = append(args, &values[i])
				}
				if err = rows.Scan(args...); err != nil {
					break
				}
				for i, field := range spec.fields {
					e.Data[field] = values[i]
				}
				switch spec.kind {
				case "pairing":
					include = include || objects[values[0]]
				case "route":
					include = include || capturedLinks[values[0]]
				case "relationship":
					include = include || objects[values[0]] || objects[values[1]]
				}
			}
			if include {
				out.Entities = append(out.Entities, e)
			}
		}
		re = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if re != nil {
			return out, re
		}
	}
	return out, nil
}
func compareReviews(before, after reviewSnapshot) ([]reviewChange, error) {
	indexes := []map[string]reviewEntity{{}, {}}
	for i, s := range []reviewSnapshot{before, after} {
		if s.Format != "mapper.topology.v1" {
			return nil, errReview("unsupported snapshot format")
		}
		for _, e := range s.Entities {
			key := e.Kind + ":" + e.ID
			if e.ID == "" || e.Kind == "" {
				return nil, errReview("snapshot entities need kind and id")
			}
			if _, exists := indexes[i][key]; exists {
				return nil, errReview("duplicate snapshot identity")
			}
			indexes[i][key] = e
		}
	}
	keys := []string{}
	for key := range indexes[0] {
		keys = append(keys, key)
	}
	for key := range indexes[1] {
		if _, exists := indexes[0][key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := []reviewChange{}
	for _, key := range keys {
		b, bok := indexes[0][key]
		a, aok := indexes[1][key]
		switch {
		case !bok:
			out = append(out, reviewChange{Type: "added", After: &a})
		case !aok:
			out = append(out, reviewChange{Type: "removed", Before: &b})
		default:
			moved := b.X != a.X || b.Y != a.Y || b.Owner != a.Owner
			for _, field := range []string{"site_id", "building_id", "floor_id", "room_id", "telecom_room_id", "rack_id", "host_id", "network"} {
				if !reflect.DeepEqual(b.Data[field], a.Data[field]) {
					moved = true
				}
			}
			if moved {
				out = append(out, reviewChange{Type: "moved", Before: &b, After: &a})
			} else if !reflect.DeepEqual(b, a) {
				out = append(out, reviewChange{Type: "changed", Before: &b, After: &a})
			}
		}
	}
	return out, nil
}

type errReview string

func (e errReview) Error() string { return string(e) }
func (a *App) reviewTopology(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/topology/snapshot" && r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	var before, after reviewSnapshot
	var err error
	if r.URL.Path == "/api/topology/snapshot" && r.Method == http.MethodGet {
		tx, e := db.Begin()
		if e != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer tx.Rollback()
		after, err = captureReview(tx, r.URL.Query().Get("network"))
		if err != nil {
			http.Error(w, "capture failed", 500)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="mapper-topology.json"`)
		json.NewEncoder(w).Encode(after)
		return
	}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		if q.Get("before_network") == "" || q.Get("after_network") == "" {
			http.Error(w, "select both networks", 400)
			return
		}
		tx, e := db.Begin()
		if e != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer tx.Rollback()
		before, err = captureReview(tx, q.Get("before_network"))
		if err == nil {
			after, err = captureReview(tx, q.Get("after_network"))
		}
		if err != nil {
			http.Error(w, "capture failed", 500)
			return
		}
	case http.MethodPost:
		var input struct {
			Before reviewSnapshot `json:"before"`
			After  reviewSnapshot `json:"after"`
		}
		dec := json.NewDecoder(r.Body)
		if err = dec.Decode(&input); err != nil {
			http.Error(w, "invalid snapshots", 400)
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			http.Error(w, "unexpected trailing data", 400)
			return
		}
		before, after = input.Before, input.After
	default:
		w.WriteHeader(405)
		return
	}
	if expected := r.URL.Query().Get("sequence"); expected != "" && expected != strconv.FormatInt(after.Sequence, 10) {
		http.Error(w, "inventory changed; restart comparison", 409)
		return
	}
	changes, err := compareReviews(before, after)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	offset := 0
	if value := r.URL.Query().Get("after"); value != "" {
		offset, err = strconv.Atoi(value)
		if err != nil || offset < 0 {
			http.Error(w, "invalid cursor", 400)
			return
		}
	}
	if offset > len(changes) {
		offset = len(changes)
	}
	end := offset + 100
	if end > len(changes) {
		end = len(changes)
	}
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.Type]++
	}
	next := ""
	if end < len(changes) {
		next = strconv.Itoa(end)
	}
	json.NewEncoder(w).Encode(map[string]any{"changes": changes[offset:end], "total": len(changes), "counts": counts, "next_cursor": next, "before_sequence": before.Sequence, "after_sequence": after.Sequence})
}
