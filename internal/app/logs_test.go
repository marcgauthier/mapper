package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcgauthier/loggy"
)

func loggedApp(t *testing.T, role string) (*App, *loggy.DB) {
	t.Helper()
	a, db := editorApp(t, role)
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	logs, err := openActionLogs(filepath.Join(t.TempDir(), "logs"), 365)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logs.Close() })
	a.setActionLogs(logs)
	return a, logs
}

func searchActions(t *testing.T, logs *loggy.DB, query string) []loggy.Entry {
	t.Helper()
	if err := logs.Flush(); err != nil {
		t.Fatal(err)
	}
	rows, err := logs.Search(context.Background(), query, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestGraphActionsAreLogged(t *testing.T) {
	a, logs := loggedApp(t, "admin")
	post := func(path, body string) {
		t.Helper()
		if w := apiRequest(a, http.MethodPost, path, body, "s"); w.Code != 204 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"one","x":10,"y":20}`)
	post("/api/objects", `{"id":"d2","kind":"device","building_id":"B1","name":"two","x":30,"y":20}`)
	post("/api/interfaces", `{"id":"p1","device_id":"d1","name":"eth0","data":{"speed":"1G"}}`)
	post("/api/interfaces", `{"id":"p2","device_id":"d2","name":"eth0","data":{"speed":"1G"}}`)
	post("/api/links", `{"id":"c1","source_interface":"p1","target_interface":"p2","data":{"connection_class":"virtual","virtual_type":"vlan","vlan_id":10}}`)
	if w := apiRequest(a, http.MethodDelete, "/api/links?id=c1", "", "s"); w.Code != 204 {
		t.Fatalf("link delete: %d %s", w.Code, w.Body.String())
	}
	rows := searchActions(t, logs, "*")
	seen := map[string]int{}
	for _, e := range rows {
		if e.Fields["actor"] != "alice" || e.Fields["service"] != "mapper" || e.Fields["detail"] == "" {
			t.Fatalf("malformed entry: %+v", e)
		}
		seen[e.Fields["action"]+"/"+e.Fields["object_id"]]++
	}
	for _, want := range []string{"object_upsert/d1", "object_upsert/d2", "interface_upsert/p1", "interface_upsert/p2", "link_create/c1", "link_delete/c1"} {
		if seen[want] != 1 {
			t.Fatalf("action %s logged %d times (%v)", want, seen[want], seen)
		}
	}
}

func TestLogsQueryBuilder(t *testing.T) {
	if q := buildLogsQuery("", "", ""); q != "* | sort by (_time) desc" {
		t.Fatalf("empty: %q", q)
	}
	if q := buildLogsQuery("alice", "link_create", ""); q != `actor:"alice" action:"link_create" | sort by (_time) desc` {
		t.Fatalf("structured: %q", q)
	}
	if q := buildLogsQuery(`a"b\c`, "", "x"); q != `actor:"a\"b\\c" "x" | sort by (_time) desc` {
		t.Fatalf("escaping: %q", q)
	}
	if q := buildLogsQuery("", "", strings.Repeat("z", 500)); !strings.HasSuffix(q, `"`+strings.Repeat("z", 200)+`" | sort by (_time) desc`) {
		t.Fatalf("text cap: %q", q)
	}
}

func TestLogsAPIRolePermissions(t *testing.T) {
	a, logs := loggedApp(t, "admin")
	a.sessions["e"] = principal{User: "ed", Role: "editor"}
	a.sessionExpires["e"] = time.Now().Add(time.Hour)

	a.sessions["v"] = principal{User: "reader", Role: "viewer"}
	a.sessionExpires["v"] = time.Now().Add(time.Hour)
	for _, session := range []string{"e", "v"} {
		if w := apiRequest(a, "GET", "/api/logs?category=user", "", session); w.Code != 200 {
			t.Fatalf("user logs %s: %d", session, w.Code)
		}
		if w := apiRequest(a, "GET", "/api/logs?category=system", "", session); w.Code != 403 {
			t.Fatalf("system logs %s: %d", session, w.Code)
		}
	}
	a.logAction("alice", "object_upsert", "d1", []byte(`{"after":{"name":"one"}}`))
	a.logAction("ed", "object_delete", "d9", []byte(`{}`))
	if err := logs.Flush(); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(a, http.MethodGet, "/api/logs", "", ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := apiRequest(a, http.MethodGet, "/api/logs", "", "e"); w.Code != 200 {
		t.Fatalf("editor: %d", w.Code)
	}
	if w := apiRequest(a, http.MethodPost, "/api/logs", "", "s"); w.Code != 405 {
		t.Fatalf("post: %d", w.Code)
	}
	if w := apiRequest(a, http.MethodGet, "/api/logs?limit=5000", "", "s"); w.Code != 400 {
		t.Fatalf("bad limit: %d", w.Code)
	}
	w := apiRequest(a, http.MethodGet, "/api/logs?actor=ed", "", "s")
	if w.Code != 200 {
		t.Fatalf("admin: %d %s", w.Code, w.Body.String())
	}
	var out []struct {
		Time    string
		Message string
		Fields  map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Fields["action"] != "object_delete" || out[0].Message != "ed object_delete d9" {
		t.Fatalf("unexpected rows: %+v", out)
	}
	if _, ok := out[0].Fields["_stream"]; ok {
		t.Fatalf("internal fields leaked: %v", out[0].Fields)
	}
	w = apiRequest(a, http.MethodGet, "/api/logs", "", "s")
	var all []struct {
		Fields map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil || len(all) != 2 {
		t.Fatalf("unfiltered: %+v %v", all, err)
	}
}

func TestLogsPageServesWhenUnlocked(t *testing.T) {
	a, _ := loggedApp(t, "admin")
	r := httptest.NewRequest(http.MethodGet, "/logs", nil)
	w := httptest.NewRecorder()
	a.logsPage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Mapper logs") {
		t.Fatalf("page: %d %.100s", w.Code, w.Body.String())
	}
	locked := &App{}
	w = httptest.NewRecorder()
	locked.logsPage(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("locked: %d", w.Code)
	}
}
