package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogCategoriesAndRequestFailures(t *testing.T) {
	a, logs := loggedApp(t, "admin")
	a.logAction("alice", "object_upsert", "device", []byte(`{"name":"Router","password":"never-save"}`))
	handler := a.observeHTTP(captureHTTPFailures(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("database unavailable") })))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/failing", nil))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	entries := searchActions(t, logs, `category:"Application logs"`)
	if len(entries) != 1 || entries[0].Fields["level"] != "error" || entries[0].Fields["stack"] == "" || entries[0].Fields["request_id"] == "" {
		t.Fatalf("missing diagnostics: %+v", entries)
	}
	for _, category := range []string{"user", "system"} {
		w = apiRequest(a, "GET", "/api/logs?category="+category, "", "s")
		if w.Code != 200 || !strings.Contains(w.Body.String(), map[string]string{"user": "User action", "system": "Application logs"}[category]) {
			t.Fatalf("%s: %d %s", category, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "never-save") {
			t.Fatal("credential logged")
		}
	}
	if w = apiRequest(a, "GET", "/api/logs?category=unknown", "", "s"); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestAuditLogsOnlyAfterCommit(t *testing.T) {
	a, logs := loggedApp(t, "admin")
	tx, err := a.ready().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.recordAudit(tx, "alice", "rolled_back", "d1", map[string]string{"name": "Router"}); err != nil {
		t.Fatal(err)
	}
	if err = a.rollbackLogged(tx); err != nil {
		t.Fatal(err)
	}
	if rows := searchActions(t, logs, "*"); len(rows) != 0 {
		t.Fatalf("rolled back action logged: %v", rows)
	}
}

func TestBrowserDiagnosticsRestrictedToSystemLogs(t *testing.T) {
	a, logs := loggedApp(t, "viewer")
	w := apiRequest(a, "POST", "/api/client-errors", `{"message":"UI failed","source":"https://mapper.test/app.js?token=private","stack":"component line 12"}`, "s")
	if w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	entries := searchActions(t, logs, `category:"Application logs"`)
	if len(entries) != 1 || entries[0].Fields["source"] != "/app.js" || entries[0].Fields["level"] != "error" {
		t.Fatalf("%+v", entries)
	}
	if w = apiRequest(a, "GET", "/api/logs?category=system", "", "s"); w.Code != 403 {
		t.Fatalf("viewer system access: %d", w.Code)
	}
	if entries := searchActions(t, logs, `category:"User action"`); len(entries) != 0 {
		t.Fatal("diagnostics entered user logs")
	}
}
