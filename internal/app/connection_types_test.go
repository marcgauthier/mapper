package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func connectionTestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	a, db := editorApp(t, "admin")
	a.sessions["e"] = principal{User: "ed", Role: "editor"}
	a.sessionExpires["e"] = time.Now().Add(time.Hour)
	a.sessions["v"] = principal{User: "vic", Role: "viewer"}
	a.sessionExpires["v"] = time.Now().Add(time.Hour)
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return a, db
}

func connectionTypeList(t *testing.T, a *App, sid string) []connectionType {
	t.Helper()
	w := apiRequest(a, http.MethodGet, "/api/connection-types", "", sid)
	if w.Code != 200 {
		t.Fatalf("list status=%d %s", w.Code, w.Body.String())
	}
	var out []connectionType
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConnectionTypesCatalogCRUD(t *testing.T) {
	a, db := connectionTestApp(t)
	if rows := connectionTypeList(t, a, "v"); len(rows) != 0 {
		t.Fatalf("fresh catalog should be empty: %v", rows)
	}
	payload := `{"id":"om5-fiber","name":"OM5 wideband fiber","connection_class":"physical","color":"#8B5CF6","sort_order":10}`
	if w := apiRequest(a, http.MethodPost, "/api/connection-types", payload, "e"); w.Code != 403 {
		t.Fatalf("editor write status=%d %s", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/connection-types", payload, "v"); w.Code != 403 {
		t.Fatalf("viewer write status=%d %s", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/connection-types", payload, "s"); w.Code != 204 {
		t.Fatalf("create status=%d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"builtin id":   `{"id":"fiber","name":"Clone fiber","connection_class":"physical"}`,
		"reserved":     `{"id":"other","name":"Other thing","connection_class":"virtual"}`,
		"bad class":    `{"id":"x1","name":"X","connection_class":"radio"}`,
		"bad color":    `{"id":"x2","name":"X","connection_class":"physical","color":"red"}`,
		"bad id":       `{"id":"Bad ID!","name":"X","connection_class":"physical"}`,
		"duplicate":    `{"id":"om5-fiber","name":"OM5 again","connection_class":"physical"}`,
		"duplicate nm": `{"id":"om5-2","name":"OM5 wideband fiber","connection_class":"physical"}`,
	} {
		if w := apiRequest(a, http.MethodPost, "/api/connection-types", body, "s"); w.Code != 400 && w.Code != 409 {
			t.Fatalf("%s status=%d %s", name, w.Code, w.Body.String())
		}
	}
	rows := connectionTypeList(t, a, "e")
	if len(rows) != 1 || rows[0].ID != "om5-fiber" || rows[0].Color != "#8B5CF6" || !rows[0].Enabled {
		t.Fatalf("unexpected catalog: %+v", rows)
	}
	update := `{"id":"om5-fiber","name":"OM5 fiber","connection_class":"physical","color":"#7C3AED","sort_order":5}`
	if w := apiRequest(a, http.MethodPut, "/api/connection-types", update, "s"); w.Code != 204 {
		t.Fatalf("update status=%d %s", w.Code, w.Body.String())
	}
	if rows := connectionTypeList(t, a, "s"); rows[0].Name != "OM5 fiber" || rows[0].SortOrder != 5 {
		t.Fatalf("update not applied: %+v", rows)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/connection-types?id=missing", "", "s"); w.Code != 404 {
		t.Fatalf("delete missing status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/connection-types?id=om5-fiber", "", "s"); w.Code != 204 {
		t.Fatalf("delete status=%d %s", w.Code, w.Body.String())
	}
	if rows := connectionTypeList(t, a, "s"); len(rows) != 0 {
		t.Fatalf("catalog not empty after delete: %v", rows)
	}
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM audit WHERE action LIKE 'connection_type_%'`).Scan(&audits); err != nil || audits < 3 {
		t.Fatalf("connection type audit rows=%d err=%v", audits, err)
	}
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE payload LIKE '%connection_types%'`).Scan(&events); err != nil || events < 3 {
		t.Fatalf("connection type event rows=%d err=%v", events, err)
	}
}

func TestConnectionTypesGuardLinks(t *testing.T) {
	a, db := connectionTestApp(t)
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d1','device','one','{}',0,0),('d2','device','two','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p1','d1','one','{}'),('p2','d2','two','{}'),('p3','d1','three','{}'),('p4','d2','four','{}'),('p5','d1','five','{}'),('p6','d2','six','{}'),('p7','d1','seven','{}'),('p8','d2','eight','{}')`,
		`INSERT INTO connection_types(id,name,connection_class,color,sort_order,enabled) VALUES('om5-fiber','OM5 fiber','physical','#7C3AED',10,1),('vxlan-evpn','VXLAN EVPN','virtual','',20,1),('tailscale','Tailscale','logical_tunnel','',30,1),('dia','Dedicated internet','provider_service','',40,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	link := func(id, src, dst, spec string) int {
		return apiRequest(a, http.MethodPost, "/api/links", `{"id":"`+id+`","source_interface":"`+src+`","target_interface":"`+dst+`","data":`+spec+`}`, "s").Code
	}
	if code := link("c1", "p1", "p2", `{"connection_class":"physical","physical_type":"om5-fiber","cable_type":"OM5","connector_a":"LC","connector_b":"LC"}`); code != 204 {
		t.Fatalf("custom physical link status=%d", code)
	}
	var stored string
	if err := db.QueryRow(`SELECT data FROM links WHERE id='c1'`).Scan(&stored); err != nil || !strings.Contains(stored, `"physical_type":"om5-fiber"`) || !strings.Contains(stored, `"medium":"om5-fiber"`) {
		t.Fatalf("custom physical spec not stored: %s %v", stored, err)
	}
	if code := link("bad1", "p3", "p4", `{"connection_class":"physical","physical_type":"nope","cable_type":"X","connector_a":"A","connector_b":"B"}`); code != 400 {
		t.Fatalf("unknown subtype status=%d", code)
	}
	if code := link("bad2", "p3", "p4", `{"connection_class":"physical","physical_type":"om5-fiber"}`); code != 400 {
		t.Fatalf("custom physical without cable details status=%d", code)
	}
	if code := link("c2", "p3", "p4", `{"connection_class":"virtual","virtual_type":"vxlan-evpn","vni":5000}`); code != 204 {
		t.Fatalf("custom virtual link status=%d", code)
	}
	if code := link("c3", "p5", "p6", `{"connection_class":"logical_tunnel","tunnel_protocol":"tailscale"}`); code != 204 {
		t.Fatalf("custom tunnel link status=%d", code)
	}
	if err := db.QueryRow(`SELECT data FROM links WHERE id='c3'`).Scan(&stored); err != nil || !strings.Contains(stored, `"tunnel_protocol":"tailscale"`) {
		t.Fatalf("custom tunnel spec not stored: %s %v", stored, err)
	}
	if code := link("c4", "p7", "p8", `{"connection_class":"provider_service","service_type":"dia","provider":"ExampleTel"}`); code != 204 {
		t.Fatalf("custom provider link status=%d", code)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/connection-types?id=om5-fiber", "", "s"); w.Code != 409 {
		t.Fatalf("delete in-use type status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodPut, "/api/connection-types", `{"id":"vxlan-evpn","name":"VXLAN EVPN","connection_class":"provider_service"}`, "s"); w.Code != 409 {
		t.Fatalf("reclassify in-use type status=%d %s", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/links?id=c1", "", "s"); w.Code != 204 {
		t.Fatalf("delete link status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/connection-types?id=om5-fiber", "", "s"); w.Code != 204 {
		t.Fatalf("delete freed type status=%d %s", w.Code, w.Body.String())
	}
}

func TestConnectionTypesCSVImport(t *testing.T) {
	a, db := connectionTestApp(t)
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('vm','virtual_machine','vm','{}',0,0),('host','device','host','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p','host','port','{}')`,
		`INSERT INTO connection_types(id,name,connection_class,sort_order,enabled) VALUES('geneve-tenant','Tenant Geneve','virtual',10,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	csv := "kind,id,name,device_id,source_interface,target_interface,connection_class,virtual_type,vni\n" +
		"interface,vi,eth0,vm,,,,,\n" +
		"connection,v9,,,vi,p,virtual,geneve-tenant,7000\n"
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		if w := apiRequest(a, http.MethodPost, path, csv, "s"); w.Code != 200 {
			t.Fatalf("%s status=%d %s", path, w.Code, w.Body.String())
		}
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM links WHERE id='v9'`).Scan(&raw); err != nil || !strings.Contains(raw, `"virtual_type":"geneve-tenant"`) || !strings.Contains(raw, `"vni":7000`) {
		t.Fatalf("imported custom spec: %s %v", raw, err)
	}
}
