package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func conduitFixtures(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	a, db := editorApp(t, "admin")
	if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES
		('site1','site','HQ','{}',0,0),
		('b1','building','B1','{"site_id":"site1"}',0,0),
		('b2','building','B2','{"site_id":"site1"}',0,0),
		('m1','manhole','MH1','{}',0,0),
		('d1','device','sw1','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return a, db
}

func TestConduitEndpointsValidate(t *testing.T) {
	a, _ := conduitFixtures(t)
	post := func(body string) (int, string) {
		w := apiRequest(a, http.MethodPost, "/api/objects", body, "s")
		return w.Code, w.Body.String()
	}
	// Legacy point conduits without endpoints keep working.
	if code, msg := post(`{"id":"c0","kind":"conduit","name":"old run","x":0,"y":0}`); code != 204 {
		t.Fatalf("legacy conduit: %d %s", code, msg)
	}
	// Names normalize to ids.
	if code, msg := post(`{"id":"c1","kind":"conduit","name":"B1-B2","endpoint_a_id":"B1","endpoint_b_id":"m1","x":0,"y":0}`); code != 204 {
		t.Fatalf("valid conduit: %d %s", code, msg)
	}
	w := apiRequest(a, http.MethodGet, "/api/objects?ids=c1", "", "s")
	var rows []struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("fetch conduit: %v %s", err, w.Body.String())
	}
	if rows[0].Data["endpoint_a_id"] != "b1" || rows[0].Data["endpoint_b_id"] != "m1" {
		t.Fatalf("endpoints not normalized: %v", rows[0].Data)
	}
	for name, body := range map[string]string{
		"device endpoint": `{"id":"cx","kind":"conduit","name":"x","endpoint_a_id":"d1","endpoint_b_id":"b1","x":0,"y":0}`,
		"missing object":  `{"id":"cx","kind":"conduit","name":"x","endpoint_a_id":"nope","endpoint_b_id":"b1","x":0,"y":0}`,
		"same endpoints":  `{"id":"cx","kind":"conduit","name":"x","endpoint_a_id":"b1","endpoint_b_id":"B1","x":0,"y":0}`,
		"conduit to self": `{"id":"c1","kind":"conduit","name":"B1-B2","endpoint_a_id":"c1","endpoint_b_id":"b1","x":0,"y":0}`,
	} {
		if code, msg := post(body); code != 400 {
			t.Fatalf("%s: %d %s", name, code, msg)
		}
	}
}

func TestConduitEndpointDeleteProtection(t *testing.T) {
	a, _ := conduitFixtures(t)
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"c1","kind":"conduit","name":"B1-B2","endpoint_a_id":"b1","endpoint_b_id":"b2","x":0,"y":0}`, "s"); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/objects?id=b1", "", "s"); w.Code != 409 {
		t.Fatalf("endpoint delete: %d %s", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/objects?id=c1", "", "s"); w.Code != 204 {
		t.Fatalf("conduit delete: %d %s", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/objects?id=b1", "", "s"); w.Code != 204 {
		t.Fatalf("freed endpoint delete: %d %s", w.Code, w.Body.String())
	}
}
