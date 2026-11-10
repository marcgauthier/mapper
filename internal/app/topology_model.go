package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var serviceCategories = map[string]bool{"cloud_provider": true, "saas": true, "paas": true, "iaas": true, "faas": true, "cloud_hosting": true, "cloud_resource": true, "internet": true, "mpls": true, "pls": true}

var siteRoles = map[string]bool{"data_center": true, "major_site": true, "branch_office": true, "edge_site": true, "disaster_recovery": true, "cloud_colo": true}

func topologyText(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}
func groundID(building string) string { return "ground:" + building }
func ensureGround(tx *sql.Tx, building string) error {
	var raw string
	if err := tx.QueryRow(`SELECT data FROM objects WHERE id=? AND kind='building'`, building).Scan(&raw); err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	data, _ := json.Marshal(map[string]any{"id": groundID(building), "kind": "floor", "name": "Ground", "building_id": building, "site_id": m["site_id"], "sort_order": 0, "auto_ground": true})
	_, err := tx.Exec(`INSERT OR IGNORE INTO objects(id,kind,name,data,x,y) VALUES(?,'floor','Ground',?,0,0)`, groundID(building), string(data))
	return err
}
func topologyObject(tx *sql.Tx, kind, ref string) (string, map[string]any, error) {
	var exact, count int
	if err := tx.QueryRow(`SELECT count(*) FROM objects WHERE kind=? AND id=?`, kind, ref).Scan(&exact); err != nil {
		return "", nil, err
	}
	if exact == 0 {
		if err := tx.QueryRow(`SELECT count(*) FROM objects WHERE kind=? AND name=?`, kind, ref).Scan(&count); err != nil {
			return "", nil, err
		}
		if count > 1 {
			return "", nil, fmt.Errorf("ambiguous %s name; select its location path", kind)
		}
	}
	var id, raw string
	if err := tx.QueryRow(`SELECT id,data FROM objects WHERE kind=? AND (id=? OR name=?) ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, kind, ref, ref, ref).Scan(&id, &raw); err != nil {
		return "", nil, fmt.Errorf("%s must reference an existing %s", kind, kind)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", nil, err
	}
	return id, m, nil
}

// New typed location fields are authoritative; legacy unresolved building names
// remain accepted so old CSV inventory is not silently assigned to a guessed site.
func normalizeTopologyPlacement(tx *sql.Tx, id, kind string, m map[string]any) error {
	if kind == "site" {
		// Unknown roles are rejected everywhere; a missing role stays
		// accepted so undo and legacy CSV imports keep working. New and
		// edited sites must carry a role through POST /api/objects.
		if role := topologyText(m, "site_role"); role != "" && !siteRoles[role] {
			return fmt.Errorf("choose a valid site role")
		}
	}
	if kind == "service" {
		if !serviceCategories[topologyText(m, "service_category")] {
			return fmt.Errorf("choose a valid service category")
		}
		for _, k := range []string{"site_id", "building_id", "floor_id", "room_id", "telecom_room_id", "rack_id", "host_id", "device_type_id"} {
			delete(m, k)
		}
		for _, k := range []string{"provider", "account", "region", "description"} {
			if s, ok := m[k].(string); ok && len(s) > 4000 {
				return fmt.Errorf("%s is too long", k)
			}
		}
		return nil
	}

	if kind == "generator" || kind == "ups" {
		if kind == "generator" && (topologyText(m, "room_id") != "" || topologyText(m, "telecom_room_id") != "" || topologyText(m, "rack_id") != "") {
			return fmt.Errorf("generators belong directly to a building")
		}
		if kind == "generator" || (topologyText(m, "room_id") == "" && topologyText(m, "telecom_room_id") == "") {
			bid, parent, e := topologyObject(tx, "building", topologyText(m, "building_id"))
			if e != nil {
				return fmt.Errorf("%s requires a building", kind)
			}
			m["building_id"] = bid
			m["site_id"] = parent["site_id"]
			for _, key := range []string{"floor_id", "room_id", "telecom_room_id", "rack_id"} {
				delete(m, key)
			}
			return nil
		}
		if topologyText(m, "rack_id") != "" {
			return fmt.Errorf("UPS placement must be a building, room or telecom room")
		}
	}
	if kind == "outlet" {
		t := topologyText(m, "outlet_type")
		if t != "dvo" && t != "fiber" {
			return fmt.Errorf("outlet_type must be dvo or fiber")
		}
	}
	if topologyText(m, "room_id") != "" && topologyText(m, "telecom_room_id") != "" {
		return fmt.Errorf("ordinary room and telecom room are mutually exclusive")
	}
	keys := []struct{ key, kind string }{{"rack_id", "rack"}, {"telecom_room_id", "telecom_room"}, {"room_id", "room"}, {"floor_id", "floor"}}
	for _, p := range keys {
		if p.kind == kind {
			continue
		}
		ref := topologyText(m, p.key)
		if ref == "" {
			continue
		}
		pid, parent, e := topologyObject(tx, p.kind, ref)
		if e != nil {
			if p.key == "telecom_room_id" && kind != "rack" && topologyText(m, "floor_id") == "" {
				continue
			}
			return e
		}
		if pid == id {
			return fmt.Errorf("an item cannot contain itself")
		}
		m[p.key] = pid
		for _, key := range []string{"site_id", "building_id", "floor_id", "room_id", "telecom_room_id"} {
			if key == p.key {
				continue
			}
			if value, ok := parent[key]; ok {
				m[key] = value
			} else if p.kind == "rack" || p.kind == "room" || p.kind == "telecom_room" {
				delete(m, key)
			}
		}
		break
	}
	if kind == "floor" {
		if topologyText(m, "building_id") == "" {
			return fmt.Errorf("floor requires a building")
		}
		bid, parent, e := topologyObject(tx, "building", topologyText(m, "building_id"))
		if e != nil {
			return e
		}
		m["building_id"] = bid
		m["site_id"] = parent["site_id"]
		for _, k := range []string{"floor_id", "room_id", "telecom_room_id", "rack_id"} {
			delete(m, k)
		}
		return nil
	}
	// DVOs may terminate a connection at a container boundary. Preserve direct
	// site/building placement rather than inventing a floor below that container.
	if kind == "outlet" && topologyText(m, "outlet_type") == "dvo" && topologyText(m, "floor_id") == "" && topologyText(m, "rack_id") == "" && topologyText(m, "room_id") == "" && topologyText(m, "telecom_room_id") == "" {
		parentKind, key := "site", "site_id"
		if topologyText(m, "building_id") != "" {
			parentKind, key = "building", "building_id"
		}
		parent, meta, err := topologyObject(tx, parentKind, topologyText(m, key))
		if err != nil {
			return err
		}
		m[key] = parent
		if parentKind == "building" {
			m["site_id"] = meta["site_id"]
		}
		return nil
	}
	indoor := kind == "room" || kind == "telecom_room" || kind == "rack" || kind == "device" || kind == "patch_panel" || kind == "outlet"
	if indoor && topologyText(m, "floor_id") == "" && topologyText(m, "building_id") != "" {
		bid, parent, e := topologyObject(tx, "building", topologyText(m, "building_id"))
		if e == nil {
			var floor string
			e = tx.QueryRow(`SELECT id FROM objects WHERE kind='floor' AND json_extract(data,'$.building_id')=? ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END,name,id LIMIT 1`, bid, groundID(bid)).Scan(&floor)
			if e == sql.ErrNoRows {
				if e = ensureGround(tx, bid); e != nil {
					return e
				}
				floor = groundID(bid)
			} else if e != nil {
				return e
			}
			m["floor_id"] = floor
			m["building_id"] = bid
			m["site_id"] = parent["site_id"]
		}
	}
	if kind == "room" && topologyText(m, "floor_id") == "" {
		return fmt.Errorf("ordinary room requires a floor")
	}
	if kind == "outlet" && topologyText(m, "floor_id") == "" && topologyText(m, "building_id") == "" {
		return fmt.Errorf("outlet requires a room or floor")
	}
	return nil
}
func ensureTopologyPorts(tx *sql.Tx, id, kind string, m map[string]any) error {
	if kind != "outlet" && kind != "service" {
		return nil
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	names := []string{"Attachment 1"}
	connector := ""
	if kind == "outlet" {
		if topologyText(m, "outlet_type") == "fiber" {
			names = []string{"Fiber 1", "Fiber 2"}
			connector = "LC"
		} else {
			names = []string{"Data", "Voice"}
			connector = "RJ45"
		}
	}
	raw, _ := json.Marshal(map[string]string{"connector": connector})
	for _, name := range names {
		if _, err := tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?)`, newID(), id, name, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
func topologyDeleteGuard(tx *sql.Tx, id, kind string) error {
	var exists int
	if e := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='relationships'`).Scan(&exists); e != nil {
		return e
	}
	if exists == 0 {
		return nil
	}
	if kind == "building" {
		if _, err := tx.Exec(`DELETE FROM objects WHERE id=? AND kind='floor' AND name='Ground' AND json_extract(data,'$.auto_ground')=1 AND NOT EXISTS(SELECT 1 FROM objects child WHERE json_extract(child.data,'$.floor_id')=?)`, groundID(id), groundID(id)); err != nil {
			return err
		}
	}
	var n int
	if e := tx.QueryRow(`SELECT count(*) FROM relationships WHERE source_id=? OR target_id=?`, id, id).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return fmt.Errorf("remove access, hosting and dependency relationships before deleting this item")
	}
	if kind == "floor" || kind == "room" {
		field := map[string]string{"floor": "floor_id", "room": "room_id"}[kind]
		if e := tx.QueryRow(`SELECT count(*) FROM objects WHERE id<>? AND json_extract(data,'$.`+field+`')=?`, id, id).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return fmt.Errorf("move contained inventory before deleting this location")
		}
	}
	return nil
}

// Keep ancestry consistent for descendants when a parent moves. The caller owns
// the transaction, audit and event; this never rewrites another user's layout.
func refreshTopologyDescendants(tx *sql.Tx, id, kind string) error {
	field := map[string]string{"site": "site_id", "building": "building_id", "floor": "floor_id", "room": "room_id", "telecom_room": "telecom_room_id", "rack": "rack_id"}[kind]
	if field == "" {
		return nil
	}
	rows, e := tx.Query(`SELECT id,kind,data FROM objects WHERE id<>? AND json_extract(data,'$.`+field+`')=? ORDER BY CASE kind WHEN 'floor' THEN 0 WHEN 'room' THEN 1 WHEN 'telecom_room' THEN 1 WHEN 'rack' THEN 2 ELSE 3 END,id`, id, id)
	if e != nil {
		return e
	}
	type child struct{ id, kind, raw string }
	items := []child{}
	for rows.Next() {
		var c child
		if e = rows.Scan(&c.id, &c.kind, &c.raw); e != nil {
			rows.Close()
			return e
		}
		items = append(items, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, c := range items {
		var m map[string]any
		_ = json.Unmarshal([]byte(c.raw), &m)
		if e = normalizeTopologyPlacement(tx, c.id, c.kind, m); e != nil {
			return e
		}
		raw, _ := json.Marshal(m)
		if string(raw) != c.raw {
			if _, e = tx.Exec(`UPDATE objects SET data=?,version=version+1 WHERE id=?`, string(raw), c.id); e != nil {
				return e
			}
		}
	}
	return nil
}
func validateServiceLink(tx *sql.Tx, source, target string, spec map[string]any) error {
	rows, e := tx.Query(`SELECT o.kind,o.data FROM objects o JOIN interfaces i ON i.device_id=o.id WHERE i.id IN (?,?)`, source, target)
	if e != nil {
		return e
	}
	type owner struct {
		kind string
		data map[string]any
	}
	owners := []owner{}
	for rows.Next() {
		var k, raw string
		if e = rows.Scan(&k, &raw); e != nil {
			rows.Close()
			return e
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		owners = append(owners, owner{k, m})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i, o := range owners {
		if o.kind != "service" {
			continue
		}
		category := topologyText(o.data, "service_category")
		if category == "internet" || category == "mpls" || category == "pls" {
			if len(owners) != 2 || owners[1-i].kind != "device" {
				return fmt.Errorf("Internet/MPLS/PLS attachments require an actual gateway device interface")
			}
			if spec["connection_class"] != "provider_service" || spec["service_type"] != category {
				return fmt.Errorf("Internet/MPLS/PLS attachments require a matching provider-service connection")
			}
		} else if spec["connection_class"] == "physical" {
			return fmt.Errorf("cloud services use access/hosting relationships or virtual/provider connections, not physical cables")
		}
	}
	return nil
}

type topologyRelationship struct {
	ID      string `json:"id"`
	Source  string `json:"source_id"`
	Target  string `json:"target_id"`
	Type    string `json:"relationship_type"`
	Version int    `json:"version"`
	Label   string `json:"label"`
}

func validateRelationship(tx *sql.Tx, x topologyRelationship) error {
	if x.Source == "" || x.Target == "" || x.Source == x.Target {
		return fmt.Errorf("choose two different relationship endpoints")
	}
	if x.Type != "accesses" && x.Type != "hosts" && x.Type != "depends_on" {
		return fmt.Errorf("relationship_type must be accesses, hosts or depends_on")
	}
	var sourceKind, targetKind string
	if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.Source).Scan(&sourceKind) != nil || tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.Target).Scan(&targetKind) != nil {
		return fmt.Errorf("relationship endpoint is missing")
	}
	if x.Type == "hosts" && (sourceKind != "service" || targetKind != "service") {
		return fmt.Errorf("hosting relationships require cloud service endpoints")
	}
	if x.Type == "accesses" && targetKind != "service" {
		return fmt.Errorf("access target must be a service")
	}
	if len(x.Label) > 200 {
		return fmt.Errorf("relationship label is too long")
	}
	if x.Type == "hosts" {
		var cycle int
		e := tx.QueryRow(`WITH RECURSIVE descendants(id) AS (SELECT ? UNION SELECT r.target_id FROM relationships r JOIN descendants d ON r.source_id=d.id WHERE r.relationship_type='hosts' AND r.id<>?) SELECT count(*) FROM descendants WHERE id=?`, x.Target, x.ID, x.Source).Scan(&cycle)
		if e != nil {
			return e
		}
		if cycle > 0 {
			return fmt.Errorf("hosting relationships cannot form a cycle")
		}
	}
	return nil
}
func (a *App) relationships(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == http.MethodGet {
		rows, e := db.Query(`SELECT id,source_id,target_id,relationship_type,version,label FROM relationships WHERE (?='' OR source_id=? OR target_id=?) AND id>? ORDER BY id LIMIT 501`, r.URL.Query().Get("object_id"), r.URL.Query().Get("object_id"), r.URL.Query().Get("object_id"), r.URL.Query().Get("after"))
		if e != nil {
			http.Error(w, "relationship query failed", 500)
			return
		}
		defer rows.Close()
		items := []topologyRelationship{}
		for rows.Next() {
			var x topologyRelationship
			if e = rows.Scan(&x.ID, &x.Source, &x.Target, &x.Type, &x.Version, &x.Label); e != nil {
				http.Error(w, "relationship read failed", 500)
				return
			}
			items = append(items, x)
		}
		next := ""
		if len(items) > 500 {
			items = items[:500]
			next = items[499].ID
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": next})
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "editor role required", 403)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	var x topologyRelationship
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&x) != nil {
		http.Error(w, "invalid relationship", 400)
		return
	}
	if x.ID == "" {
		x.ID = newID()
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var before topologyRelationship
	lookup := tx.QueryRow(`SELECT id,source_id,target_id,relationship_type,version,label FROM relationships WHERE id=?`, x.ID).Scan(&before.ID, &before.Source, &before.Target, &before.Type, &before.Version, &before.Label)
	if lookup != nil && lookup != sql.ErrNoRows {
		http.Error(w, "relationship lookup failed", 500)
		return
	}
	if lookup == sql.ErrNoRows {
		var used int
		if err := tx.QueryRow(`SELECT count(*) FROM audit WHERE object_id=? AND action IN ('relationship','relationship_undo','relationship_import')`, x.ID).Scan(&used); err != nil {
			http.Error(w, "relationship history unavailable", 500)
			return
		}
		if used > 0 {
			http.Error(w, "this relationship ID was previously used; create a new relationship", 409)
			return
		}
	}
	if x.Version != before.Version {
		http.Error(w, "relationship changed; reload and retry", 409)
		return
	}
	if r.Method == http.MethodDelete {
		if lookup != nil {
			http.Error(w, "relationship missing", 404)
			return
		}
		x.Version = before.Version + 1
		_, e = tx.Exec(`DELETE FROM relationships WHERE id=?`, x.ID)
	} else {
		if err := validateRelationship(tx, x); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		x.Version++
		_, e = tx.Exec(`INSERT INTO relationships(id,source_id,target_id,relationship_type,version,label) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_id=excluded.source_id,target_id=excluded.target_id,relationship_type=excluded.relationship_type,version=excluded.version,label=excluded.label`, x.ID, x.Source, x.Target, x.Type, x.Version, x.Label)
	}
	if e == nil {
		e = a.recordAudit(tx, p.User, "relationship", x.ID, map[string]any{"before": before, "after": x, "deleted": r.Method == http.MethodDelete})
	}
	event, _ := json.Marshal(map[string]string{"type": "relationship", "id": x.ID})
	var seq int64
	if e == nil {
		seq, e = appendEvent(tx, event)
	}
	if e != nil || a.commitLogged(tx) != nil {
		http.Error(w, "relationship save failed", 500)
		return
	}
	a.notify(seq, event)
	json.NewEncoder(w).Encode(x)
}
func (a *App) convertService(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" {
		http.Error(w, "editor required", 403)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var x struct {
		ID       string `json:"id"`
		Version  int    `json:"version"`
		Category string `json:"service_category"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || !serviceCategories[x.Category] {
		http.Error(w, "choose a service category", 400)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var kind, raw, name string
	var version int
	if tx.QueryRow(`SELECT kind,name,data,version FROM objects WHERE id=?`, x.ID).Scan(&kind, &name, &raw, &version) != nil {
		http.Error(w, "item missing", 404)
		return
	}
	if version != x.Version {
		http.Error(w, "item changed", 409)
		return
	}
	if kind != "device" {
		http.Error(w, "only cloud-type devices can be converted", 400)
		return
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	cloudTypes := map[string]bool{"cloud": true, "cloud-provider": true, "google-cloud": true, "azure": true, "aws": true, "oracle-cloud": true, "saas": true, "paas": true, "iaas": true, "faas": true}
	if !cloudTypes[topologyText(m, "device_type_id")] && !cloudTypes[topologyText(m, "device_visual")] {
		http.Error(w, "item is not a cloud catalog type", 400)
		return
	}
	var n int
	if e = tx.QueryRow(`SELECT count(*) FROM links l JOIN interfaces i ON i.id=l.source_interface OR i.id=l.target_interface WHERE i.device_id=? AND COALESCE(json_extract(l.data,'$.connection_class'),'physical')='physical'`, x.ID).Scan(&n); e != nil || n > 0 {
		http.Error(w, "resolve physical connections before converting this cloud item", 409)
		return
	}
	if e = tx.QueryRow(`SELECT count(*) FROM objects WHERE json_extract(data,'$.host_id')=?`, x.ID).Scan(&n); e != nil || n > 0 {
		http.Error(w, "move hosted VMs before conversion", 409)
		return
	}
	if x.Category == "internet" || x.Category == "mpls" || x.Category == "pls" {
		if err := tx.QueryRow(`SELECT count(*) FROM links l JOIN interfaces i ON i.id=l.source_interface OR i.id=l.target_interface WHERE i.device_id=? AND (COALESCE(json_extract(l.data,'$.connection_class'),'physical')<>'provider_service' OR COALESCE(json_extract(l.data,'$.service_type'),'')<>?)`, x.ID, x.Category).Scan(&n); err != nil || n > 0 {
			http.Error(w, "resolve incompatible connections before converting to an Internet/MPLS/PLS service", 409)
			return
		}
	}
	m["service_category"] = x.Category
	m["kind"] = "service"
	m["name"] = name
	m["id"] = x.ID
	if e = normalizeTopologyPlacement(tx, x.ID, "service", m); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	after, _ := json.Marshal(m)
	_, e = tx.Exec(`UPDATE objects SET kind='service',data=?,version=version+1 WHERE id=?`, string(after), x.ID)
	if e == nil {
		e = a.recordAudit(tx, p.User, "service_conversion", x.ID, map[string]any{"before": json.RawMessage(raw), "after": m})
	}
	event, _ := json.Marshal(map[string]string{"type": "service_conversion", "id": x.ID})
	var seq int64
	if e == nil {
		seq, e = appendEvent(tx, event)
	}
	if e != nil || a.commitLogged(tx) != nil {
		http.Error(w, "conversion failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}

func (a *App) serviceAttachment(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" {
		http.Error(w, "editor required", 403)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var x struct {
		Gateway   string `json:"gateway_interface"`
		Service   string `json:"service_id"`
		Circuit   string `json:"circuit_id"`
		Bandwidth string `json:"bandwidth"`
		Label     string `json:"label"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		http.Error(w, "invalid attachment", 400)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var kind, raw string
	if tx.QueryRow(`SELECT o.kind FROM objects o JOIN interfaces i ON i.device_id=o.id WHERE i.id=?`, x.Gateway).Scan(&kind) != nil || kind != "device" {
		http.Error(w, "select an actual gateway device interface", 400)
		return
	}
	if tx.QueryRow(`SELECT data FROM objects WHERE id=? AND kind='service'`, x.Service).Scan(&raw) != nil {
		http.Error(w, "service missing", 400)
		return
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	category := topologyText(m, "service_category")
	if category != "internet" && category != "mpls" && category != "pls" {
		http.Error(w, "choose an Internet, MPLS or PLS service", 400)
		return
	}
	port, id := newID(), newID()
	if _, e = tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,'{}')`, port, x.Service, "Attachment "+id[:8]); e != nil {
		http.Error(w, "attachment port creation failed", 500)
		return
	}
	spec := map[string]any{"connection_class": "provider_service", "service_type": category, "circuit_id": x.Circuit, "bandwidth": x.Bandwidth, "label": x.Label, "provider": topologyText(m, "provider")}
	if e = checkLinkAvailability(tx, x.Gateway, port, spec, ""); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	data, _ := json.Marshal(spec)
	if _, e = tx.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES(?,?,?,?)`, id, x.Gateway, port, string(data)); e != nil {
		http.Error(w, "attachment save failed", 500)
		return
	}
	if e = a.recordAudit(tx, p.User, "service_attachment", id, map[string]any{"gateway_interface": x.Gateway, "service_interface": port, "service_id": x.Service, "data": spec}); e != nil {
		http.Error(w, "audit failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "service_attachment", "id": id})
	seq, e := appendEvent(tx, event)
	if e != nil || a.commitLogged(tx) != nil {
		http.Error(w, "attachment commit failed", 500)
		return
	}
	a.notify(seq, event)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}
func (a *App) assignConduit(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" {
		http.Error(w, "editor required", 403)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var x struct {
		Conduit  string `json:"conduit_id"`
		Link     string `json:"link_id"`
		Sequence int64  `json:"sequence"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		http.Error(w, "invalid cable assignment", 400)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var kind string
	var seq int64
	if tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq) != nil {
		http.Error(w, "event cursor missing", 500)
		return
	}
	if x.Sequence != seq {
		http.Error(w, "topology changed; refresh before assigning the cable", 409)
		return
	}
	if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.Conduit).Scan(&kind) != nil || kind != "conduit" {
		http.Error(w, "conduit missing", 400)
		return
	}
	var raw string
	if tx.QueryRow(`SELECT data FROM links WHERE id=?`, x.Link).Scan(&raw) != nil || jsonClass(raw) != "physical" {
		http.Error(w, "choose a physical cable", 400)
		return
	}
	var n int
	if tx.QueryRow(`SELECT count(*) FROM cable_routes WHERE link_id=? AND infrastructure_id=?`, x.Link, x.Conduit).Scan(&n) != nil {
		http.Error(w, "route lookup failed", 500)
		return
	}
	if n > 0 {
		http.Error(w, "cable already belongs to this conduit", 409)
		return
	}
	var order int
	if e = tx.QueryRow(`SELECT COALESCE(MAX(sequence),-1)+1 FROM cable_routes WHERE link_id=?`, x.Link).Scan(&order); e != nil {
		http.Error(w, "route order unavailable", 500)
		return
	}
	_, e = tx.Exec(`INSERT INTO cable_routes(id,link_id,sequence,infrastructure_id) VALUES(?,?,?,?)`, newID(), x.Link, order, x.Conduit)
	if e == nil {
		e = a.recordAudit(tx, p.User, "conduit_assignment", x.Link, map[string]any{"conduit_id": x.Conduit, "sequence": order})
	}
	event, _ := json.Marshal(map[string]string{"type": "route", "link_id": x.Link})
	if e == nil {
		seq, e = appendEvent(tx, event)
	}
	if e != nil || a.commitLogged(tx) != nil {
		http.Error(w, "assignment failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}

func topologyDependentStamp(tx *sql.Tx, id, kind string) (string, error) {
	field := map[string]string{"site": "site_id", "building": "building_id", "floor": "floor_id", "room": "room_id", "telecom_room": "telecom_room_id", "rack": "rack_id", "device": "host_id"}[kind]
	if field == "" {
		return "", nil
	}
	var count, total int64
	e := tx.QueryRow(`SELECT count(*),COALESCE(sum(version),0) FROM objects WHERE id<>? AND json_extract(data,'$.`+field+`')=?`, id, id).Scan(&count, &total)
	return fmt.Sprintf("%d:%d", count, total), e
}

func (a *App) undoRelationship(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" || r.Method != http.MethodPost {
		http.Error(w, "editor POST required", 403)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var aid int64
	var id, raw string
	if tx.QueryRow(`SELECT id,object_id,data FROM audit WHERE actor=? AND action='relationship' ORDER BY id DESC LIMIT 1`, p.User).Scan(&aid, &id, &raw) != nil {
		http.Error(w, "no relationship edit to undo", 404)
		return
	}
	var latest int64
	if tx.QueryRow(`SELECT id FROM audit WHERE object_id=? ORDER BY id DESC LIMIT 1`, id).Scan(&latest) != nil || latest != aid {
		http.Error(w, "relationship changed; undo is unsafe", 409)
		return
	}
	var snapshot struct {
		Before  topologyRelationship `json:"before"`
		After   topologyRelationship `json:"after"`
		Deleted bool                 `json:"deleted"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		http.Error(w, "invalid undo snapshot", 409)
		return
	}
	var current int
	lookup := tx.QueryRow(`SELECT version FROM relationships WHERE id=?`, id).Scan(&current)
	if snapshot.Deleted {
		if lookup != sql.ErrNoRows {
			http.Error(w, "relationship was recreated; undo is unsafe", 409)
			return
		}
	} else if lookup != nil || current != snapshot.After.Version {
		http.Error(w, "relationship changed; undo is unsafe", 409)
		return
	}
	if snapshot.Before.ID == "" {
		_, e = tx.Exec(`DELETE FROM relationships WHERE id=?`, id)
	} else {
		x := snapshot.Before
		if err := validateRelationship(tx, x); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		version := current + 1
		if snapshot.Deleted {
			version = snapshot.After.Version + 1
		}
		_, e = tx.Exec(`INSERT INTO relationships(id,source_id,target_id,relationship_type,version,label) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_id=excluded.source_id,target_id=excluded.target_id,relationship_type=excluded.relationship_type,version=excluded.version,label=excluded.label`, x.ID, x.Source, x.Target, x.Type, version, x.Label)
	}
	if e == nil {
		e = a.recordAudit(tx, p.User, "relationship_undo", id, map[string]any{"undo_of": aid, "before": snapshot.After, "after": snapshot.Before})
	}
	event, _ := json.Marshal(map[string]string{"type": "relationship", "id": id})
	var seq int64
	if e == nil {
		seq, e = appendEvent(tx, event)
	}
	if e != nil || a.commitLogged(tx) != nil {
		http.Error(w, "undo failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}
