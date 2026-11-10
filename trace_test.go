package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcgauthier/libmurmur/clients/go/murmurdb"
)

func traceFixture(t *testing.T) *App {
	t.Helper()
	key := strings.Repeat("42", 32)
	db, err := murmurdb.Open(filepath.Join(t.TempDir(), "trace-store") + "?key=" + key + "&mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE objects(id TEXT PRIMARY KEY,kind TEXT,name TEXT)`,
		`CREATE TABLE interfaces(id TEXT PRIMARY KEY,device_id TEXT,name TEXT)`,
		`CREATE TABLE links(id TEXT PRIMARY KEY,source_interface TEXT,target_interface TEXT,data TEXT DEFAULT '{}')`,
		`CREATE TABLE cable_routes(id TEXT PRIMARY KEY,link_id TEXT,sequence INTEGER,infrastructure_id TEXT)`,
		`CREATE TABLE patch_pairings(id TEXT PRIMARY KEY,panel_id TEXT,port_a TEXT UNIQUE,port_b TEXT UNIQUE)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return &App{db: db}
}

func TestUnlockRateLimitIsPerPeer(t *testing.T) {
	a := &App{unlockAttempts: map[string][]time.Time{}}
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(`{"key":"x"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		w := httptest.NewRecorder()
		a.unlock(w, r)
		if w.Code != 400 {
			t.Fatalf("attempt %d returned %d", i+1, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(`{"key":"long-enough"}`))
	r.RemoteAddr = "192.0.2.1:9999"
	w := httptest.NewRecorder()
	a.unlock(w, r)
	if w.Code != 429 {
		t.Fatalf("expected rate limit, got %d", w.Code)
	}
}

func TestTraceAcrossMultiplePatchPanels(t *testing.T) {
	a := traceFixture(t)
	db := a.db
	objects := [][3]string{{"edge-a", "device", "Edge A"}, {"panel-a", "patch_panel", "Panel A"}, {"panel-b", "patch_panel", "Panel B"}, {"edge-b", "device", "Edge B"}, {"duct", "conduit", "Duct 7"}}
	for _, v := range objects {
		if _, e := db.Exec(`INSERT INTO objects VALUES(?,?,?)`, v[0], v[1], v[2]); e != nil {
			t.Fatal(e)
		}
	}
	ports := [][3]string{{"start", "edge-a", "xe0"}, {"a1", "panel-a", "1"}, {"a2", "panel-a", "2"}, {"b1", "panel-b", "1"}, {"b2", "panel-b", "2"}, {"finish", "edge-b", "xe0"}}
	for _, v := range ports {
		if _, e := db.Exec(`INSERT INTO interfaces VALUES(?,?,?)`, v[0], v[1], v[2]); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range [][3]string{{"c1", "start", "a1"}, {"c2", "a2", "b1"}, {"c3", "b2", "finish"}} {
		if _, e := db.Exec(`INSERT INTO links(id,source_interface,target_interface) VALUES(?,?,?)`, v[0], v[1], v[2]); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range [][4]string{{"pa", "panel-a", "a1", "a2"}, {"pb", "panel-b", "b1", "b2"}} {
		if _, e := db.Exec(`INSERT INTO patch_pairings VALUES(?,?,?,?)`, v[0], v[1], v[2], v[3]); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := db.Exec(`INSERT INTO cable_routes VALUES('r1','c2',0,'duct')`); e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/api/trace?interface_id=start", nil)
	w := httptest.NewRecorder()
	a.trace(w, r, db)
	if w.Code != 200 {
		t.Fatalf("trace status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Destination string `json:"destination_interface"`
		Segments    []struct {
			Link  string              `json:"link_id"`
			Route []map[string]string `json:"route"`
		} `json:"segments"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if got.Destination != "finish" || len(got.Segments) != 3 {
		t.Fatalf("unexpected trace: %+v", got)
	}
	if len(got.Segments[1].Route) != 1 || got.Segments[1].Route[0]["name"] != "Duct 7" {
		t.Fatalf("missing cable route in trace: %+v", got.Segments[1])
	}
}

func TestTraceRejectsUnpairedPatchPort(t *testing.T) {
	a := traceFixture(t)
	db := a.db
	for _, v := range [][3]string{{"d", "device", "D"}, {"p", "patch_panel", "P"}} {
		db.Exec(`INSERT INTO objects VALUES(?,?,?)`, v[0], v[1], v[2])
	}
	db.Exec(`INSERT INTO interfaces VALUES('i','d','xe0')`)
	db.Exec(`INSERT INTO interfaces VALUES('p1','p','1')`)
	db.Exec(`INSERT INTO links(id,source_interface,target_interface) VALUES('c','i','p1')`)
	r := httptest.NewRequest("GET", "/api/trace?interface_id=i", nil)
	w := httptest.NewRecorder()
	a.trace(w, r, db)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "unpaired") {
		t.Fatalf("expected unpaired-port conflict, got %d %s", w.Code, w.Body.String())
	}
}
