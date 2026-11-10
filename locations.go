package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type managedNetwork struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
	Enabled     bool   `json:"enabled"`
}

// networks serves the managed network catalog backing the header network
// filter and the item editor picker. Items reference networks by id, with
// legacy free-text names still matching by name. Like locations, editors and
// administrators manage the catalog while viewers browse it.
func (a *App) networks(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == http.MethodGet {
		rows, err := db.Query(`SELECT id,name,description,sort_order,enabled FROM networks WHERE enabled=1 ORDER BY sort_order,name`)
		if err != nil {
			http.Error(w, "network catalog unavailable", 500)
			return
		}
		defer rows.Close()
		out := []managedNetwork{}
		for rows.Next() {
			var item managedNetwork
			var enabled int
			if rows.Scan(&item.ID, &item.Name, &item.Description, &item.SortOrder, &enabled) == nil {
				item.Enabled = enabled != 0
				out = append(out, item)
			}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		id := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("id")))
		if id == "" {
			http.Error(w, "network id required", http.StatusBadRequest)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		var name string
		if err = tx.QueryRow(`SELECT name FROM networks WHERE id=?`, id).Scan(&name); err != nil {
			http.Error(w, "network not found", 404)
			return
		}
		if networkInUse(tx, id, name) {
			http.Error(w, "network is in use; remove it from its items before deleting it", http.StatusConflict)
			return
		}
		if _, err = tx.Exec(`DELETE FROM networks WHERE id=?`, id); err != nil {
			http.Error(w, "network delete failed", 500)
			return
		}
		if err = a.recordAudit(tx, p.User, "network_delete", id, map[string]any{"id": id}); err != nil {
			http.Error(w, "network audit failed", 500)
			return
		}
		event := []byte(fmt.Sprintf(`{"type":"networks","action":"delete","id":%q}`, id))
		seq, err := appendEvent(tx, event)
		if err != nil || a.commitLogged(tx) != nil {
			http.Error(w, "network update failed", 500)
			return
		}
		a.notify(seq, event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var item managedNetwork
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item) != nil {
		http.Error(w, "invalid network", http.StatusBadRequest)
		return
	}
	item.ID = strings.TrimSpace(strings.ToLower(item.ID))
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	if !validDeviceTypeID(item.ID) || item.Name == "" || len(item.Name) > 80 || len(item.Description) > 500 {
		http.Error(w, "network requires a valid id, a name, and a short description", http.StatusBadRequest)
		return
	}
	if item.SortOrder < 0 || item.SortOrder > 10000 {
		http.Error(w, "sort_order must be between 0 and 10000", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && item.SortOrder == 0 {
		item.SortOrder = 100
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	action := "network_create"
	var before any
	if r.Method == http.MethodPost {
		_, err = tx.Exec(`INSERT INTO networks(id,name,description,sort_order,enabled) VALUES(?,?,?,?,1)`, item.ID, item.Name, item.Description, item.SortOrder)
	} else {
		action = "network_update"
		var old managedNetwork
		var enabled int
		if err = tx.QueryRow(`SELECT id,name,description,sort_order,enabled FROM networks WHERE id=?`, item.ID).Scan(&old.ID, &old.Name, &old.Description, &old.SortOrder, &enabled); err != nil {
			http.Error(w, "network not found", 404)
			return
		}
		old.Enabled = enabled != 0
		before = old
		if old.Name != item.Name {
			if err = rewriteNetworkName(tx, old.Name, item.ID); err != nil {
				http.Error(w, "network rename failed", 500)
				return
			}
		}
		_, err = tx.Exec(`UPDATE networks SET name=?,description=?,sort_order=? WHERE id=?`, item.Name, item.Description, item.SortOrder, item.ID)
	}
	if err != nil {
		http.Error(w, "network save failed; check for duplicate IDs or names", http.StatusConflict)
		return
	}
	if err = a.recordAudit(tx, p.User, action, item.ID, map[string]any{"before": before, "after": item}); err != nil {
		http.Error(w, "network audit failed", 500)
		return
	}
	event := []byte(fmt.Sprintf(`{"type":"networks","action":%q,"id":%q}`, action, item.ID))
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "network event failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "network update failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func networkInUse(q queryRower, id, name string) bool {
	var used int
	err := q.QueryRow(`SELECT count(*) FROM objects o WHERE json_valid(o.data) AND (
		(json_type(o.data,'$.network')='text' AND trim(json_extract(o.data,'$.network')) IN (?,?))
		OR (json_type(o.data,'$.network')='array' AND EXISTS(SELECT 1 FROM json_each(o.data,'$.network') AS je WHERE trim(je.value) IN (?,?))))`, id, name, id, name).Scan(&used)
	return err == nil && used != 0
}

// rewriteNetworkName canonicalizes legacy name references to the stable id so
// a rename never orphans items.
func rewriteNetworkName(tx *sql.Tx, oldName, id string) error {
	rows, err := tx.Query(`SELECT o.id,o.data FROM objects o WHERE json_valid(o.data) AND (
		(json_type(o.data,'$.network')='text' AND trim(json_extract(o.data,'$.network'))=?)
		OR (json_type(o.data,'$.network')='array' AND EXISTS(SELECT 1 FROM json_each(o.data,'$.network') AS je WHERE trim(je.value)=?)))`, oldName, oldName)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rewrite struct{ id, data string }
	var pending []rewrite
	for rows.Next() {
		var objectID, raw string
		if rows.Scan(&objectID, &raw) != nil {
			continue
		}
		var decoded map[string]any
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			continue
		}
		switch network := decoded["network"].(type) {
		case string:
			if strings.TrimSpace(network) == oldName {
				decoded["network"] = id
			}
		case []any:
			for i, entry := range network {
				if name, ok := entry.(string); ok && strings.TrimSpace(name) == oldName {
					network[i] = id
				}
			}
		default:
			continue
		}
		updated, err := json.Marshal(decoded)
		if err != nil {
			return err
		}
		pending = append(pending, rewrite{objectID, string(updated)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range pending {
		if _, err := tx.Exec(`UPDATE objects SET data=? WHERE id=?`, item.data, item.id); err != nil {
			return err
		}
	}
	return nil
}

// A rack is the placement authority for assigned devices. Location names remain
// accepted for legacy inventory; rack assignment requires an existing rack.
func normalizeRackPlacement(tx *sql.Tx, kind string, meta map[string]any) error {
	ref, _ := meta["rack_id"].(string)
	if ref == "" || kind == "rack" {
		return nil
	}
	var id, raw string
	if tx.QueryRow(`SELECT id,data FROM objects WHERE kind='rack' AND (id=? OR name=?) ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, ref, ref, ref).Scan(&id, &raw) != nil {
		return fmt.Errorf("rack_id must reference an existing rack")
	}
	var rack map[string]any
	if json.Unmarshal([]byte(raw), &rack) != nil {
		return fmt.Errorf("rack metadata is unavailable")
	}
	meta["rack_id"] = id
	for _, key := range []string{"site_id", "building_id", "floor_id", "room_id", "telecom_room_id"} {
		if value, exists := rack[key]; exists {
			meta[key] = value
		} else {
			delete(meta, key)
		}
	}
	return nil
}

// Square hierarchy: every placed object names its square. Buildings name a
// site; telecom rooms name a building; racks name a building or telecom
// room; equipment names a building, telecom room, or rack. Names need not
// resolve yet (CSV imports carry free-text locations), but the field must
// be present: placement is what nests an item inside its square on the map.
// Infrastructure (conduit, manhole), annotations (label, shape), sites, and
// hosted items (which follow their hypervisor) are exempt.
func requirePlacement(kind string, meta map[string]any) error {
	present := func(key string) bool {
		s, _ := meta[key].(string)
		return strings.TrimSpace(s) != ""
	}
	switch kind {
	case "device", "patch_panel":
		if !present("rack_id") && !present("telecom_room_id") && !present("building_id") {
			return fmt.Errorf("equipment must be placed in a building, telecom room, or rack")
		}
	case "building":
		if !present("site_id") {
			return fmt.Errorf("building requires a site")
		}
	case "telecom_room":
		if !present("building_id") {
			return fmt.Errorf("telecom room requires a building")
		}
	case "rack":
		if !present("telecom_room_id") && !present("building_id") {
			return fmt.Errorf("rack requires a telecom room or building")
		}
	}
	return nil
}

// A conduit spans two endpoints (sites, buildings, telecom rooms, or
// manholes) and renders as a line between them. Endpoints are optional so
// legacy point conduits keep working; when set they must be distinct and
// reference existing span-capable objects. Names are accepted and normalized
// to ids like other location references. A conduit can never terminate at
// itself: conduits are not span-capable, so a self reference fails lookup.
func normalizeConduitEndpoints(tx *sql.Tx, kind string, meta map[string]any) error {
	if kind != "conduit" {
		return nil
	}
	ids := map[string]string{}
	for _, key := range []string{"endpoint_a_id", "endpoint_b_id"} {
		ref, _ := meta[key].(string)
		if ref == "" {
			delete(meta, key)
			continue
		}
		var id string
		if tx.QueryRow(`SELECT id FROM objects WHERE kind IN ('site','building','floor','room','telecom_room','manhole') AND (id=? OR name=?) ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, ref, ref, ref).Scan(&id) != nil {
			return fmt.Errorf("%s must reference an existing site, building, telecom room, or manhole", key)
		}
		ids[key] = id
		meta[key] = id
	}
	if a, b := ids["endpoint_a_id"], ids["endpoint_b_id"]; a != "" && a == b {
		return fmt.Errorf("conduit endpoints must be distinct")
	}
	return nil
}

func normalizeLocationParent(tx *sql.Tx, kind string, meta map[string]any) error {
	parentKey, parentKind := "", ""
	switch kind {
	case "building":
		parentKey, parentKind = "site_id", "site"
	case "telecom_room":
		if topologyText(meta, "floor_id") != "" {
			return nil
		}
		parentKey, parentKind = "building_id", "building"
	case "rack":
		if topologyText(meta, "room_id") != "" || topologyText(meta, "floor_id") != "" {
			return nil
		}
		parentKey, parentKind = "telecom_room_id", "telecom_room"
	default:
		return nil
	}
	ref, _ := meta[parentKey].(string)
	if ref == "" {
		return nil
	}
	var id, raw string
	if tx.QueryRow(`SELECT id,data FROM objects WHERE kind=? AND (id=? OR name=?) ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, parentKind, ref, ref, ref).Scan(&id, &raw) != nil {
		return fmt.Errorf("%s must reference an existing %s", parentKey, parentKind)
	}
	var parent map[string]any
	_ = json.Unmarshal([]byte(raw), &parent)
	meta[parentKey] = id
	for _, key := range []string{"site_id", "building_id"} {
		if key == parentKey {
			continue
		}
		if value, exists := parent[key]; exists {
			meta[key] = value
		}
	}
	return nil
}

// Prevent a container category change from orphaning placed inventory.
func locationChildCount(tx *sql.Tx, id string) (int, error) {
	var count int
	err := tx.QueryRow(`SELECT count(*) FROM objects child JOIN objects parent ON parent.id=? WHERE parent.kind IN ('site','building','floor','room','telecom_room','rack') AND child.id<>parent.id AND (json_extract(child.data,'$.site_id') IN (parent.id,parent.name) OR json_extract(child.data,'$.building_id') IN (parent.id,parent.name) OR json_extract(child.data,'$.telecom_room_id') IN (parent.id,parent.name) OR json_extract(child.data,'$.rack_id') IN (parent.id,parent.name) OR json_extract(child.data,'$.floor_id') IN (parent.id,parent.name) OR json_extract(child.data,'$.room_id') IN (parent.id,parent.name))`, id).Scan(&count)
	return count, err
}
