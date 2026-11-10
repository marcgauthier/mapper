package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

const populateTestBody = `{"sites":2,"buildings_per_site":2,"rooms_per_building":3,"telecom_rooms_per_building":1,"devices_min_per_room":3,"devices_max_per_room":3,"seed":7}`

func populateTableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, query := range map[string]string{
		"objects":        `SELECT count(*) FROM objects`,
		"interfaces":     `SELECT count(*) FROM interfaces`,
		"links":          `SELECT count(*) FROM links`,
		"relationships":  `SELECT count(*) FROM relationships`,
		"cable_routes":   `SELECT count(*) FROM cable_routes`,
		"patch_pairings": `SELECT count(*) FROM patch_pairings`,
		"networks":       `SELECT count(*) FROM networks`,
	} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

func TestPopulateDemoData(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(a, http.MethodPost, "/api/populate", populateTestBody, "s")
	if w.Code != 200 {
		t.Fatalf("populate: %d %s", w.Code, w.Body.String())
	}
	var summary struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]int{
		"site": 2, "building": 4, "floor": 4, "room": 8, "telecom_room": 4,
		"rack": 8, "device": 40, "virtual_machine": 8, "virtual_switch": 4,
		"patch_panel": 8, "ups": 8, "generator": 3, "outlet": 8,
		"manhole": 2, "conduit": 6, "service": 7, "networks": 3, "relationships": 5,
	} {
		if summary.Counts[kind] != want {
			t.Errorf("count %s = %d, want %d", kind, summary.Counts[kind], want)
		}
	}
	if summary.Counts["links"] == 0 || summary.Counts["interfaces"] == 0 || summary.Counts["pairings"] == 0 {
		t.Fatalf("missing links/interfaces/pairings: %v", summary.Counts)
	}
	var tunnels, provider, virtual, routes int
	for class, dest := range map[string]*int{"logical_tunnel": &tunnels, "provider_service": &provider, "virtual": &virtual} {
		if err := db.QueryRow(`SELECT count(*) FROM links WHERE json_extract(data,'$.connection_class')=?`, class).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if tunnels != 1 {
		t.Errorf("tunnel links = %d, want 1", tunnels)
	}
	if provider != 6 {
		t.Errorf("provider attachment links = %d, want 6", provider)
	}
	if virtual != 12 {
		t.Errorf("virtual links = %d, want 12", virtual)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cable_routes`).Scan(&routes); err != nil || routes != 4 {
		t.Errorf("cable routes = %d (err %v), want 4", routes, err)
	}
	var dangling int
	if err := db.QueryRow(`SELECT count(*) FROM cable_routes r LEFT JOIN objects o ON o.id=r.infrastructure_id AND o.kind='conduit' WHERE o.id IS NULL`).Scan(&dangling); err != nil || dangling != 0 {
		t.Errorf("dangling cable routes = %d (err %v)", dangling, err)
	}
	var badEndpoints int
	if err := db.QueryRow(`SELECT count(*) FROM objects c LEFT JOIN objects a ON a.id=json_extract(c.data,'$.endpoint_a_id') LEFT JOIN objects b ON b.id=json_extract(c.data,'$.endpoint_b_id') WHERE c.kind='conduit' AND (a.id IS NULL OR b.id IS NULL OR a.id=b.id)`).Scan(&badEndpoints); err != nil || badEndpoints != 0 {
		t.Errorf("bad conduit endpoints = %d (err %v)", badEndpoints, err)
	}
	var hosted int
	if err := db.QueryRow(`SELECT count(*) FROM objects vm JOIN objects h ON h.id=json_extract(vm.data,'$.host_id') WHERE vm.kind='virtual_machine' AND h.kind='device'`).Scan(&hosted); err != nil || hosted != 8 {
		t.Errorf("hosted VMs = %d (err %v), want 8", hosted, err)
	}
	var declared int
	if err := db.QueryRow(`SELECT count(*) FROM objects WHERE kind='device' AND json_array_length(json_extract(data,'$.vrfs'))=4`).Scan(&declared); err != nil || declared < 8 {
		t.Errorf("devices declaring 4 VRFs = %d (err %v), want at least 8", declared, err)
	}
	var multiVRF int
	if err := db.QueryRow(`SELECT count(*) FROM interfaces WHERE json_array_length(json_extract(data,'$.vrfs'))>1`).Scan(&multiVRF); err != nil || multiVRF != 0 {
		t.Errorf("interfaces with several VRFs = %d (err %v)", multiVRF, err)
	}
	for _, typ := range []string{"hosts", "accesses", "depends_on"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM relationships WHERE relationship_type=?`, typ).Scan(&n); err != nil || n == 0 {
			t.Errorf("relationship %s count = %d (err %v)", typ, n, err)
		}
	}
	// Re-running with the same options upserts the same rows.
	before := populateTableCounts(t, db)
	if w := apiRequest(a, http.MethodPost, "/api/populate", populateTestBody, "s"); w.Code != 200 {
		t.Fatalf("re-populate: %d %s", w.Code, w.Body.String())
	}
	after := populateTableCounts(t, db)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("table %s grew %d -> %d on re-run", table, n, after[table])
		}
	}
}

func TestPopulateReplacesPreviousScope(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(a, http.MethodPost, "/api/populate", populateTestBody, "s"); w.Code != 200 {
		t.Fatalf("populate: %d %s", w.Code, w.Body.String())
	}
	full := populateTableCounts(t, db)
	small := `{"sites":1,"buildings_per_site":1,"rooms_per_building":1,"telecom_rooms_per_building":1,"devices_min_per_room":1,"devices_max_per_room":1,"seed":3}`
	if w := apiRequest(a, http.MethodPost, "/api/populate", small, "s"); w.Code != 200 {
		t.Fatalf("re-populate smaller: %d %s", w.Code, w.Body.String())
	}
	var sites, second int
	if err := db.QueryRow(`SELECT count(*) FROM objects WHERE kind='site'`).Scan(&sites); err != nil || sites != 1 {
		t.Fatalf("sites = %d (err %v), want 1", sites, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM objects WHERE id LIKE 'pop:s02%'`).Scan(&second); err != nil || second != 0 {
		t.Fatalf("leftover second-site rows = %d (err %v)", second, err)
	}
	var dangling int
	if err := db.QueryRow(`SELECT count(*) FROM links l LEFT JOIN interfaces s ON s.id=l.source_interface LEFT JOIN interfaces d ON d.id=l.target_interface WHERE s.id IS NULL OR d.id IS NULL`).Scan(&dangling); err != nil || dangling != 0 {
		t.Errorf("dangling links = %d (err %v)", dangling, err)
	}
	// Regenerating the original options restores the original footprint.
	if w := apiRequest(a, http.MethodPost, "/api/populate", populateTestBody, "s"); w.Code != 200 {
		t.Fatalf("re-populate full: %d %s", w.Code, w.Body.String())
	}
	after := populateTableCounts(t, db)
	for table, n := range full {
		if after[table] != n {
			t.Errorf("table %s = %d, want %d", table, after[table], n)
		}
	}
}

func TestPopulateRejectsBadOptions(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"too many sites": `{"sites":999}`,
		"no telecom cap": `{"rooms_per_building":5,"telecom_rooms_per_building":6}`,
		"inverted devs":  `{"devices_min_per_room":5,"devices_max_per_room":2}`,
		"malformed":      `{oops`,
	} {
		if w := apiRequest(a, http.MethodPost, "/api/populate", body, "s"); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := apiRequest(a, http.MethodGet, "/api/populate", "", "s"); w.Code != 405 {
		t.Errorf("GET: %d %s", w.Code, w.Body.String())
	}
}

func TestPopulateRequiresEditor(t *testing.T) {
	a, db := editorApp(t, "viewer")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(a, http.MethodPost, "/api/populate", `{"sites":1}`, "s"); w.Code != 403 {
		t.Errorf("viewer: %d %s", w.Code, w.Body.String())
	}
}
