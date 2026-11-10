package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func apiRequest(a *App, method, path, body, sid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: "mapper_session", Value: sid})
	}
	w := httptest.NewRecorder()
	a.api(w, r)
	return w
}

func editorApp(t *testing.T, role string) (*App, *sql.DB) {
	t.Helper()
	db := scaleDB(t)
	a := &App{db: db, sessions: map[string]principal{"s": {User: "alice", Role: role}}, sessionExpires: map[string]time.Time{"s": time.Now().Add(time.Hour)}, clients: map[chan []byte]bool{}}
	return a, db
}

func TestUnlockCreatesAdminAndWrongKeyCannotReplaceStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapper-store")
	t.Setenv("MAPPER_DB", path)
	newApp := func() *App {
		return &App{unlockAttempts: map[string][]time.Time{}, loginAttempts: map[string][]time.Time{}, sessions: map[string]principal{}, sessionExpires: map[string]time.Time{}}
	}
	unlock := func(a *App, key, user, password string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"key": key, "username": user, "password": password})
		r := httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(string(body)))
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		a.unlock(w, r)
		return w
	}
	first := newApp()
	if w := unlock(first, "test-database-key", "admin", "long-enough-password"); w.Code != http.StatusOK {
		t.Fatalf("first unlock: %d %s", w.Code, w.Body.String())
	}
	var admins int
	if err := first.ready().QueryRow(`SELECT count(*) FROM users WHERE role='admin'`).Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("initial administrator count=%d err=%v", admins, err)
	}
	loginReq := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"long-enough-password"}`))
	loginReq.RemoteAddr = "127.0.0.1:4321"
	loginW := httptest.NewRecorder()
	first.login(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("password login status=%d %s", loginW.Code, loginW.Body.String())
	}
	cookie := loginW.Result().Cookies()
	if len(cookie) != 1 || !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("login did not set expected session cookie: %#v", cookie)
	}
	_ = first.ready().Close()
	if w := unlock(newApp(), "wrong-database-key", "other", "another-long-password"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key unlock status=%d body=%s", w.Code, w.Body.String())
	}
	second := newApp()
	if w := unlock(second, "test-database-key", "", ""); w.Code != http.StatusOK {
		t.Fatalf("unlock after wrong-key attempt: %d %s", w.Code, w.Body.String())
	}
	if err := second.ready().QueryRow(`SELECT count(*) FROM users WHERE username='admin' AND role='admin'`).Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("existing admin not preserved: count=%d err=%v", admins, err)
	}
	_ = second.ready().Close()
}

func TestObjectVersionsRejectStaleConcurrentEdits(t *testing.T) {
	a, db := editorApp(t, "editor")
	create := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"switch-1","x":10,"y":20}`, "s")
	if create.Code != http.StatusNoContent {
		t.Fatalf("create status=%d %s", create.Code, create.Body.String())
	}
	update := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"switch-new","x":10,"y":20,"version":1}`, "s")
	if update.Code != http.StatusNoContent {
		t.Fatalf("first update status=%d %s", update.Code, update.Body.String())
	}
	stale := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"stale-write","x":10,"y":20,"version":1}`, "s")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d want 409: %s", stale.Code, stale.Body.String())
	}
	var name string
	var version int
	if err := db.QueryRow(`SELECT name,version FROM objects WHERE id='d1'`).Scan(&name, &version); err != nil || name != "switch-new" || version != 2 {
		t.Fatalf("stale update changed record: name=%s version=%d err=%v", name, version, err)
	}
}

func TestFiberLinkRequiresAndStoresEndpointSpecifications(t *testing.T) {
	a, db := editorApp(t, "editor")
	for _, object := range [][3]string{{"d1", "device", "one"}, {"d2", "device", "two"}} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,'{}',0,0)`, object[0], object[1], object[2]); err != nil {
			t.Fatal(err)
		}
	}
	for _, port := range [][3]string{{"p1", "d1", "xe0"}, {"p2", "d2", "xe0"}, {"p3", "d1", "xe1"}, {"p4", "d2", "xe1"}} {
		if _, err := db.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,'{}')`, port[0], port[1], port[2]); err != nil {
			t.Fatal(err)
		}
	}
	missing := apiRequest(a, http.MethodPost, "/api/links", `{"source_interface":"p1","target_interface":"p2","data":{}}`, "s")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("link without cable spec status=%d", missing.Code)
	}
	body := `{"id":"c1","source_interface":"p1","target_interface":"p2","data":{"medium":"fiber","cable_type":"OS2","connector_a":"LC","connector_b":"SC","strand_count":12,"length_m":40}}`
	created := apiRequest(a, http.MethodPost, "/api/links", body, "s")
	if created.Code != http.StatusNoContent {
		t.Fatalf("link create status=%d %s", created.Code, created.Body.String())
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM links WHERE id='c1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"medium": "fiber", "cable_type": "OS2", "connector_a": "LC", "connector_b": "SC", "strand_count": float64(12), "length_m": float64(40)} {
		if got[key] != want {
			t.Errorf("link %s=%v want %v", key, got[key], want)
		}
	}
	duplicate := apiRequest(a, http.MethodPost, "/api/links", strings.Replace(body, `"id":"c1"`, `"id":"c2"`, 1), "s")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("reused interface status=%d want 409", duplicate.Code)
	}
}

func TestDeleteAndUndoProtectReferencedTopology(t *testing.T) {
	a, db := editorApp(t, "editor")
	created := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"router","x":1,"y":2}`, "s")
	if created.Code != http.StatusNoContent {
		t.Fatal(created.Code, created.Body.String())
	}
	if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d2','device','switch-2','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, port := range [][3]string{{"p1", "d1", "xe0"}, {"p2", "d2", "xe0"}} {
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{}')`, port[0], port[1], port[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO links VALUES('c1','p1','p2','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit(actor,action,object_id,data,created) VALUES('alice','interface_upsert','p1','{}',?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	undo := apiRequest(a, http.MethodPost, "/api/undo", "", "s")
	if undo.Code != http.StatusConflict {
		t.Fatalf("undo deleted dependent device: status=%d %s", undo.Code, undo.Body.String())
	}
	blocked := apiRequest(a, http.MethodDelete, "/api/objects?id=d1", "", "s")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("delete allowed a connected device: status=%d %s", blocked.Code, blocked.Body.String())
	}
	removeCable := apiRequest(a, http.MethodDelete, "/api/links?id=c1", "", "s")
	if removeCable.Code != http.StatusNoContent {
		t.Fatalf("cable delete status=%d %s", removeCable.Code, removeCable.Body.String())
	}
	remove := apiRequest(a, http.MethodDelete, "/api/objects?id=d1", "", "s")
	if remove.Code != http.StatusNoContent {
		t.Fatalf("safe object delete status=%d %s", remove.Code, remove.Body.String())
	}
}

func TestViewerCannotMutateAndUserChangesAreAuditedTransactionally(t *testing.T) {
	a, db := editorApp(t, "admin")
	if _, err := db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,username TEXT,password TEXT,role TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users VALUES('a1','alice','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	created := apiRequest(a, http.MethodPost, "/api/users", `{"username":"bob","password":"a-long-password","role":"viewer"}`, "s")
	if created.Code != http.StatusCreated {
		t.Fatalf("user create status=%d %s", created.Code, created.Body.String())
	}
	var action string
	if err := db.QueryRow(`SELECT action FROM audit WHERE object_id='bob'`).Scan(&action); err != nil || action != "user_post" {
		t.Fatalf("user mutation audit action=%q err=%v", action, err)
	}
	demote := apiRequest(a, http.MethodPut, "/api/users", `{"username":"alice","role":"editor"}`, "s")
	if demote.Code != http.StatusConflict {
		t.Fatalf("last admin demotion status=%d", demote.Code)
	}
	viewer := &App{db: db, sessions: map[string]principal{"v": {User: "bob", Role: "viewer"}}, sessionExpires: map[string]time.Time{"v": time.Now().Add(time.Hour)}}
	response := apiRequest(viewer, http.MethodPost, "/api/objects", `{"id":"x","kind":"device","building_id":"B1","name":"x","x":0,"y":0}`, "v")
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation status=%d", response.Code)
	}
}

func TestUndoRestoresLatestUncontestedObjectEdit(t *testing.T) {
	a, db := editorApp(t, "editor")
	if response := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"before","x":10,"y":20}`, "s"); response.Code != http.StatusNoContent {
		t.Fatalf("create status=%d %s", response.Code, response.Body.String())
	}
	if response := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d1","kind":"device","building_id":"B1","name":"after","x":30,"y":40,"version":1}`, "s"); response.Code != http.StatusNoContent {
		t.Fatalf("update status=%d %s", response.Code, response.Body.String())
	}
	if response := apiRequest(a, http.MethodPost, "/api/undo", "", "s"); response.Code != http.StatusNoContent {
		t.Fatalf("undo status=%d %s", response.Code, response.Body.String())
	}
	var name string
	var x, y float64
	var version int
	if err := db.QueryRow(`SELECT name,x,y,version FROM objects WHERE id='d1'`).Scan(&name, &x, &y, &version); err != nil {
		t.Fatal(err)
	}
	if name != "before" || x != 10 || y != 20 || version != 3 {
		t.Fatalf("undo state name=%q pos=(%v,%v) version=%d", name, x, y, version)
	}
	if response := apiRequest(a, http.MethodPost, "/api/undo", "", "s"); response.Code != http.StatusConflict {
		t.Fatalf("repeated unsafe undo status=%d", response.Code)
	}
}

func TestLocationSearchFindsAndReturnsReferencedDevice(t *testing.T) {
	a, db := editorApp(t, "editor")
	for _, object := range []struct {
		id, kind, name, data string
	}{
		{"site-1", "site", "North Campus", `{"site_id":"site-1"}`},
		{"device-1", "device", "edge-17", `{"site_id":"site-1","device_type":"switch"}`},
	} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?)`, object.id, object.kind, object.name, object.data, 100.0, 120.0); err != nil {
			t.Fatal(err)
		}
	}
	hits := apiRequest(a, http.MethodGet, "/api/search?q=North%20Campus&grouping=location", "", "s")
	if hits.Code != http.StatusOK {
		t.Fatalf("location search status=%d %s", hits.Code, hits.Body.String())
	}
	var found []map[string]any
	if err := json.Unmarshal(hits.Body.Bytes(), &found); err != nil || len(found) != 1 || found[0]["id"] != "site-1" {
		t.Fatalf("location search result=%v err=%v", found, err)
	}
	for _, query := range []struct {
		term  string
		count int
	}{{"North%20Campus", 0}, {"edge", 1}} {
		response := apiRequest(a, http.MethodGet, "/api/search?scope=devices&q="+query.term, "", "s")
		var matches []map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &matches); response.Code != http.StatusOK || err != nil || len(matches) != query.count {
			t.Fatalf("device-only search %s: status=%d results=%v err=%v", query.term, response.Code, matches, err)
		}
	}
	members := apiRequest(a, http.MethodGet, "/api/search?members_kind=site&members_id=site-1&members_name=North%20Campus&grouping=location", "", "s")
	if members.Code != http.StatusOK {
		t.Fatalf("location member lookup status=%d %s", members.Code, members.Body.String())
	}
	var devices []map[string]any
	if err := json.Unmarshal(members.Body.Bytes(), &devices); err != nil || len(devices) != 1 || devices[0]["id"] != "device-1" {
		t.Fatalf("location members=%v err=%v", devices, err)
	}
}

func TestInterfacesRequireDeviceOrPatchPanelOwner(t *testing.T) {
	a, db := editorApp(t, "editor")
	for _, object := range [][3]string{{"site-1", "site", "site"}, {"device-1", "device", "switch"}} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,'{}',0,0)`, object[0], object[1], object[2]); err != nil {
			t.Fatal(err)
		}
	}
	invalid := apiRequest(a, http.MethodPost, "/api/interfaces", `{"device_id":"site-1","name":"bad-port"}`, "s")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("site interface status=%d want 400", invalid.Code)
	}
	missing := apiRequest(a, http.MethodPost, "/api/interfaces", `{"device_id":"missing","name":"bad-port"}`, "s")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing owner status=%d want 400", missing.Code)
	}
	valid := apiRequest(a, http.MethodPost, "/api/interfaces", `{"device_id":"device-1","name":"xe0","data":{"connector":"SFP+","speed":"1G"}}`, "s")
	if valid.Code != http.StatusNoContent {
		t.Fatalf("device interface status=%d %s", valid.Code, valid.Body.String())
	}
}

func TestViewportIncludesLinksCrossingToRemoteDevices(t *testing.T) {
	a, db := editorApp(t, "viewer")
	for _, object := range [][3]string{{"d1", "device", "near"}, {"d2", "device", "far"}} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,'{}',500,500)`, object[0], object[1], object[2]); err != nil {
			t.Fatal(err)
		}
	}
	for _, port := range [][3]string{{"p1", "d1", "xe0"}, {"p2", "d2", "xe0"}} {
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{}')`, port[0], port[1], port[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO links VALUES('c1','p1','p2','{"medium":"fiber"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO layouts VALUES('network','d1',10,10),('network','d2',1000,10)`); err != nil {
		t.Fatal(err)
	}
	links := apiRequest(a, http.MethodGet, "/api/links?min_x=0&max_x=20&min_y=0&max_y=20&grouping=network", "", "s")
	if links.Code != http.StatusOK {
		t.Fatalf("link viewport status=%d %s", links.Code, links.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(links.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["id"] != "c1" {
		t.Fatalf("boundary-crossing link missing: %v err=%v", got, err)
	}
	remote := apiRequest(a, http.MethodGet, "/api/objects?ids=d2&grouping=network", "", "s")
	if remote.Code != http.StatusOK {
		t.Fatalf("remote endpoint lookup status=%d %s", remote.Code, remote.Body.String())
	}
	var objects []map[string]any
	if err := json.Unmarshal(remote.Body.Bytes(), &objects); err != nil || len(objects) != 1 || objects[0]["x"] != float64(1000) {
		t.Fatalf("remote endpoint position=%v err=%v", objects, err)
	}
}

func TestViewportIncludesLinkWhoseRouteCrossesViewport(t *testing.T) {
	a, db := editorApp(t, "viewer")
	for _, object := range [][4]any{{"d1", "device", "near", 500.0}, {"d2", "device", "far", 1000.0}, {"conduit-1", "conduit", "route conduit", 15.0}} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,'{}',?,10)`, object[0], object[1], object[2], object[3]); err != nil {
			t.Fatal(err)
		}
	}
	for _, port := range [][3]string{{"p1", "d1", "xe0"}, {"p2", "d2", "xe0"}} {
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{}')`, port[0], port[1], port[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO links VALUES('c1','p1','p2','{"medium":"fiber"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cable_routes(id,link_id,sequence,infrastructure_id) VALUES('r1','c1',0,'conduit-1')`); err != nil {
		t.Fatal(err)
	}
	links := apiRequest(a, http.MethodGet, "/api/links?min_x=0&max_x=20&min_y=0&max_y=20&grouping=location", "", "s")
	if links.Code != http.StatusOK {
		t.Fatalf("link viewport status=%d %s", links.Code, links.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(links.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["id"] != "c1" {
		t.Fatalf("route-crossing link missing from viewport: %v err=%v", got, err)
	}
}
