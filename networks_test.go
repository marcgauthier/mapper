package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func networkCatalog(t *testing.T, a *App, sid string) []managedNetwork {
	t.Helper()
	w := apiRequest(a, http.MethodGet, "/api/networks", "", sid)
	if w.Code != 200 {
		t.Fatalf("list status=%d %s", w.Code, w.Body.String())
	}
	var out []managedNetwork
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNetworksCatalogCRUD(t *testing.T) {
	a, db := editorApp(t, "admin")
	a.sessions["e"] = principal{User: "ed", Role: "editor"}
	a.sessionExpires["e"] = time.Now().Add(time.Hour)
	a.sessions["v"] = principal{User: "vic", Role: "viewer"}
	a.sessionExpires["v"] = time.Now().Add(time.Hour)
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if rows := networkCatalog(t, a, "v"); len(rows) != 0 {
		t.Fatalf("fresh catalog should be empty: %v", rows)
	}
	payload := `{"id":"core-01","name":"CORE-01","description":"Backbone","sort_order":10}`
	if w := apiRequest(a, http.MethodPost, "/api/networks", payload, "v"); w.Code != 403 {
		t.Fatalf("viewer write status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodPost, "/api/networks", payload, "e"); w.Code != 204 {
		t.Fatalf("editor create status=%d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"duplicate": `{"id":"core-01","name":"Other","description":""}`,
		"dup name":  `{"id":"other","name":"CORE-01","description":""}`,
		"bad id":    `{"id":"Bad ID!","name":"X"}`,
		"no name":   `{"id":"x1","name":""}`,
	} {
		if w := apiRequest(a, http.MethodPost, "/api/networks", body, "s"); w.Code != 400 && w.Code != 409 {
			t.Fatalf("%s status=%d %s", name, w.Code, w.Body.String())
		}
	}
	rows := networkCatalog(t, a, "e")
	if len(rows) != 1 || rows[0].ID != "core-01" || rows[0].Description != "Backbone" || !rows[0].Enabled {
		t.Fatalf("unexpected catalog: %+v", rows)
	}
	update := `{"id":"core-01","name":"CORE-01","description":"Backbone ring","sort_order":5}`
	if w := apiRequest(a, http.MethodPut, "/api/networks", update, "s"); w.Code != 204 {
		t.Fatalf("update status=%d %s", w.Code, w.Body.String())
	}
	if rows := networkCatalog(t, a, "s"); rows[0].Description != "Backbone ring" || rows[0].SortOrder != 5 {
		t.Fatalf("update not applied: %+v", rows)
	}
	if w := apiRequest(a, http.MethodPut, "/api/networks", `{"id":"missing","name":"X"}`, "s"); w.Code != 404 {
		t.Fatalf("update missing status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/networks?id=missing", "", "s"); w.Code != 404 {
		t.Fatalf("delete missing status=%d", w.Code)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/networks?id=core-01", "", "s"); w.Code != 204 {
		t.Fatalf("delete status=%d %s", w.Code, w.Body.String())
	}
	if rows := networkCatalog(t, a, "s"); len(rows) != 0 {
		t.Fatalf("catalog not empty after delete: %v", rows)
	}
	var audits, events int
	if err := db.QueryRow(`SELECT count(*) FROM audit WHERE action LIKE 'network_%'`).Scan(&audits); err != nil || audits < 3 {
		t.Fatalf("network audit rows=%d err=%v", audits, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE payload LIKE '%"networks"%'`).Scan(&events); err != nil || events < 3 {
		t.Fatalf("network event rows=%d err=%v", events, err)
	}
}

func TestNetworksBootstrapAndReferences(t *testing.T) {
	a, db := editorApp(t, "admin")
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d1','device','a','{"network":["CORE-01"," Voice "]}',0,0),('d2','device','b','{"network":"DMZ"}',0,0),('d3','device','c','{"network":["CORE-01"]}',0,0),('d4','device','d','{}',0,0),('s1','site','HQ','{}',0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	rows := networkCatalog(t, a, "s")
	got := map[string]string{}
	for _, row := range rows {
		got[row.ID] = row.Name
	}
	for id, name := range map[string]string{"core-01": "CORE-01", "voice": "Voice", "dmz": "DMZ"} {
		if got[id] != name {
			t.Fatalf("bootstrapped catalog=%v", got)
		}
	}
	if w := apiRequest(a, http.MethodDelete, "/api/networks?id=core-01", "", "s"); w.Code != 409 {
		t.Fatalf("delete in-use status=%d", w.Code)
	}
	rename := `{"id":"dmz","name":"Demilitarized","description":"","sort_order":100}`
	if w := apiRequest(a, http.MethodPut, "/api/networks", rename, "s"); w.Code != 204 {
		t.Fatalf("rename status=%d %s", w.Code, w.Body.String())
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM objects WHERE id='d2'`).Scan(&raw); err != nil || !strings.Contains(raw, `"network":"dmz"`) {
		t.Fatalf("rename did not canonicalize reference: %s %v", raw, err)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/networks?id=voice", "", "s"); w.Code != 409 {
		t.Fatalf("delete array-referenced status=%d", w.Code)
	}
}
