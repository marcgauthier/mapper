package app

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDiagramAfterGeographicWorkspaceRemoval(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('s','site','HQ','{}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('b','building','B1','{"site_id":"s"}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('r','telecom_room','Room','{"site_id":"s","building_id":"b"}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('a','device','A','{"site_id":"s","building_id":"b","telecom_room_id":"r"}',10,20)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('z','device','Z','{"site_id":"s","building_id":"b"}',30,40)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('duct','conduit','Duct','{}',0,0)`,
		`INSERT INTO interfaces VALUES('ap','a','port','{}')`,
		`INSERT INTO interfaces VALUES('zp','z','port','{}')`,
		`INSERT INTO links VALUES('cable','ap','zp','{"connection_class":"physical","medium":"backplane"}')`,
		`INSERT INTO geography VALUES('object','a','{"building_id":"b","floor_id":"ground","parent_id":"r","local":[1,2]}',1,'b','ground','r',0,0,0,0)`,
		`INSERT INTO geography VALUES('link','cable','{"sections":[],"strands":[]}',1,'','','',0,0,0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/map-config", "/api/geography", "/api/geography/unplaced", "/api/geography/undo"} {
		w := apiRequest(a, http.MethodGet, path, "", "s")
		if w.Code != 404 {
			t.Fatalf("%s status=%d %s", path, w.Code, w.Body.String())
		}
	}
	w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"a","kind":"device","name":"A","site_id":"s","building_id":"b","telecom_room_id":"","x":100,"y":200}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPost, "/api/routes", `{"link_id":"cable","infrastructure_ids":["duct"]}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPut, "/api/links", `{"id":"cable","source_interface":"ap","target_interface":"zp","data":{"medium":"backplane","label":"Edited in diagram"}}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM geography`).Scan(&count); err != nil || count != 2 {
		t.Fatal("archived metadata changed", count, err)
	}
	html, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"maplibre", "geographic.start", "/geographic.js", "/geographic.css", "window.geographic"} {
		if strings.Contains(string(html), removed) {
			t.Fatalf("removed integration remains: %s", removed)
		}
	}
}

func TestArchivedGeographyCSVRoundTrip(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	raw := `{"floors":[{"id":"upper","name":"Upper"}],"unknown_future_field":{"keep":true},"footprint":[[[1,2],[3,4],[5,6],[1,2]]]}`
	if _, err := db.Exec(`INSERT INTO geography VALUES('object','legacy',?,7,'','','',1,2,5,6)`, raw); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal("existing stores must reopen", err)
	}
	exported := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	target, targetDB := editorApp(t, "editor")
	if err := migrateSchema(targetDB); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"/api/import?dry_run=1", "/api/import"} {
		result := apiRequest(target, http.MethodPost, url, exported.Body.String(), "s")
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
		if strings.Contains(url, "dry_run") {
			var n int
			_ = targetDB.QueryRow(`SELECT count(*) FROM geography`).Scan(&n)
			if n != 0 {
				t.Fatal("preview committed archive")
			}
		}
	}
	var got string
	if err := targetDB.QueryRow(`SELECT data FROM geography WHERE owner_id='legacy'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var expected, actual any
	_ = json.Unmarshal([]byte(raw), &expected)
	_ = json.Unmarshal([]byte(got), &actual)
	e, _ := json.Marshal(expected)
	g, _ := json.Marshal(actual)
	if string(e) != string(g) {
		t.Fatalf("archive changed: %s", got)
	}
}

func TestLegacyMapStyleConfigIsIgnored(t *testing.T) {
	cfg := defaultConfig()
	if err := parseConfigYAML("map_style: https://example.invalid/style.json\naddr: :9090\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" {
		t.Fatal(cfg)
	}
}
