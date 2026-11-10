package main

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcgauthier/libmurmur/clients/go/murmurdb"
)

func scaleDB(t testing.TB) *sql.DB {
	t.Helper()
	db, e := murmurdb.Open(filepath.Join(t.TempDir(), "scale-store") + "?key=" + strings.Repeat("73", 32) + "&mode=rwc")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, e = db.Exec(`CREATE TABLE objects(id TEXT PRIMARY KEY,kind TEXT,name TEXT,data TEXT,x REAL,y REAL,version INTEGER DEFAULT 1)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`CREATE INDEX objects_bounds_idx ON objects(x,y)`); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT,action TEXT,object_id TEXT,data TEXT,created INTEGER)`, `CREATE TABLE events(seq INTEGER PRIMARY KEY AUTOINCREMENT,payload TEXT NOT NULL,created INTEGER NOT NULL)`, `CREATE TABLE interfaces(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,name TEXT NOT NULL,data TEXT NOT NULL,UNIQUE(device_id,name))`, `CREATE TABLE links(id TEXT PRIMARY KEY,source_interface TEXT UNIQUE,target_interface TEXT UNIQUE,data TEXT)`, `CREATE TABLE patch_pairings(id TEXT PRIMARY KEY,panel_id TEXT,port_a TEXT UNIQUE,port_b TEXT UNIQUE)`, `CREATE TABLE cable_routes(id TEXT PRIMARY KEY,link_id TEXT,sequence INTEGER,infrastructure_id TEXT)`, `CREATE TABLE layouts(grouping TEXT,object_id TEXT,x REAL,y REAL,PRIMARY KEY(grouping,object_id))`} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	return db
}

func TestMTLSIdentityMapsVerifiedCertificateToUser(t *testing.T) {
	db := scaleDB(t)
	if _, e := db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,username TEXT,password TEXT,role TEXT)`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO users VALUES('u1','alice','unused','editor')`); e != nil {
		t.Fatal(e)
	}
	a := &App{db: db, sessions: map[string]principal{}, sessionExpires: map[string]time.Time{}}
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "alice"}}
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	w := httptest.NewRecorder()
	a.me(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"User":"alice"`) || !strings.Contains(w.Body.String(), `"Role":"editor"`) {
		t.Fatalf("mTLS identity failed: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestConcurrentObjectWritesCommitAuditAndEvents(t *testing.T) {
	db := scaleDB(t)
	const editors, writesPerEditor = 100, 10
	totalWrites := editors * writesPerEditor
	a := &App{db: db, clients: map[chan []byte]bool{}}
	a.sessions = make(map[string]principal, editors)
	a.sessionExpires = make(map[string]time.Time, editors)
	for i := 0; i < editors; i++ {
		sid := fmt.Sprintf("s-%d", i)
		a.sessions[sid] = principal{User: sid, Role: "editor"}
		a.sessionExpires[sid] = time.Now().Add(time.Hour)
	}
	var wg sync.WaitGroup
	errs := make(chan int, totalWrites)
	for editor := 0; editor < editors; editor++ {
		wg.Add(1)
		go func(editor int) {
			defer wg.Done()
			sid := fmt.Sprintf("s-%d", editor)
			for edit := 0; edit < writesPerEditor; edit++ {
				id := fmt.Sprintf("d-%d-%d", editor, edit)
				body := fmt.Sprintf(`{"id":%q,"kind":"device","name":"sw-%s","building_id":"B1","x":%d,"y":%d}`, id, id, editor, edit)
				r := httptest.NewRequest(http.MethodPost, "/api/objects", strings.NewReader(body))
				r.AddCookie(&http.Cookie{Name: "mapper_session", Value: sid})
				w := httptest.NewRecorder()
				a.api(w, r)
				if w.Code != http.StatusNoContent {
					errs <- w.Code
				}
			}
		}(editor)
	}
	wg.Wait()
	close(errs)
	for code := range errs {
		t.Errorf("concurrent object write returned HTTP %d", code)
	}
	for _, table := range []string{"objects", "audit", "events"} {
		var n int
		if e := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != totalWrites {
			t.Errorf("%s rows=%d want=%d", table, n, totalWrites)
		}
	}
}

func TestViewportObjectQueryBoundsResults(t *testing.T) {
	db := scaleDB(t)
	for i, p := range [][2]float64{{0, 0}, {10, 10}, {100, 100}} {
		if _, e := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?)`, fmt.Sprint(i), "device", fmt.Sprintf("d%d", i), "{}", p[0], p[1]); e != nil {
			t.Fatal(e)
		}
	}
	a := &App{db: db, sessions: map[string]principal{"s": {User: "test", Role: "viewer"}}, sessionExpires: map[string]time.Time{"s": time.Now().Add(time.Hour)}}
	r := httptest.NewRequest("GET", "/api/objects?min_x=5&max_x=20&min_y=5&max_y=20", nil)
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w := httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0]["id"] != "1" {
		t.Fatalf("unexpected bounded result: %#v", rows)
	}
}

func TestCSVImportPreservesTopologyMetadata(t *testing.T) {
	db := scaleDB(t)
	a := &App{db: db, sessions: map[string]principal{"s": {User: "ed", Role: "editor"}}, sessionExpires: map[string]time.Time{"s": time.Now().Add(time.Hour)}}
	csvData := "kind,id,name,device_type,site_id,building_id,telecom_room_id,network,supportgroup\ndevice,d1,sw1,switch,S1,B1,R1,backbone,netops\n"
	r := httptest.NewRequest(http.MethodPost, "/api/import?dry_run=1", strings.NewReader(csvData))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w := httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 200 {
		t.Fatalf("preview status=%d: %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(csvData))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w = httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 200 {
		t.Fatalf("import status=%d: %s", w.Code, w.Body.String())
	}
	var raw string
	if e := db.QueryRow(`SELECT data FROM objects WHERE id='d1'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var got map[string]string
	if e := json.Unmarshal([]byte(raw), &got); e != nil {
		t.Fatal(e)
	}
	for k, v := range map[string]string{"device_type": "switch", "site_id": "S1", "building_id": "B1", "telecom_room_id": "R1", "network": "backbone", "supportgroup": "netops"} {
		if got[k] != v {
			t.Errorf("%s=%q want %q", k, got[k], v)
		}
	}
}

func TestCSVImportsInterfacesConnectionsPairingsAndRoutesAtomically(t *testing.T) {
	db := scaleDB(t)
	a := &App{db: db, sessions: map[string]principal{"s": {User: "ed", Role: "editor"}}, sessionExpires: map[string]time.Time{"s": time.Now().Add(time.Hour)}}
	data := "kind,id,name,device_id,source_interface,target_interface,panel_id,port_a,port_b,link_id,infrastructure_id,sequence,medium,cable_type,connector_a,connector_b,building_id\n" +
		"patch_panel,panel,PP1,,,,,,,,,,,,,,B1\n" + "conduit,duct,Duct 1,,,,,,,,,\n" + "device,d1,Edge A,,,,,,,,,,,,,,B1\n" + "device,d2,Edge B,,,,,,,,,,,,,,B1\n" + "interface,i1,xe0,d1,,,,,,,,\n" + "interface,p1,1,panel,,,,,,,,\n" + "interface,p2,2,panel,,,,,,,,\n" + "interface,i2,xe0,d2,,,,,,,,\n" + "pairing,pair1,,,,,panel,p1,p2,,,\n" + "connection,c1,,,i1,p1,,,,,,,fiber,OS2,LC,LC\n" + "connection,c2,,,p2,i2,,,,,,,fiber,OS2,LC,LC\n" + "route,r1,,,,,,,,c2,duct,0\n"
	invalid := strings.Replace(data, "connection,c2,,,p2,i2", "connection,c2,,,p2,missing-interface", 1)
	r := httptest.NewRequest(http.MethodPost, "/api/import?dry_run=1", strings.NewReader(invalid))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w := httptest.NewRecorder()
	a.api(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid-reference preview status=%d body=%s", w.Code, w.Body.String())
	}
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM objects`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("invalid preview wrote objects: count=%d err=%v", n, e)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/import?dry_run=1", strings.NewReader(data))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w = httptest.NewRecorder()
	a.api(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("valid preview status=%d body=%s", w.Code, w.Body.String())
	}
	for _, table := range []string{"objects", "interfaces", "links", "patch_pairings", "cable_routes", "audit", "events"} {
		if e := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("dry run wrote %s: count=%d err=%v", table, n, e)
		}
	}
	r = httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(data))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "s"})
	w = httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 200 {
		t.Fatalf("import status=%d: %s", w.Code, w.Body.String())
	}
	for table, want := range map[string]int{"objects": 4, "interfaces": 4, "links": 2, "patch_pairings": 1, "cable_routes": 1, "events": 1} {
		var n int
		if e := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != want {
			t.Errorf("%s count=%d want=%d", table, n, want)
		}
	}
}

func BenchmarkViewportAPI100K(b *testing.B) {
	db := scaleDB(b)
	tx, e := db.Begin()
	if e != nil {
		b.Fatal(e)
	}
	stmt, e := tx.Prepare(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?)`)
	if e != nil {
		b.Fatal(e)
	}
	for i := 0; i < 100000; i++ {
		if _, e = stmt.Exec(fmt.Sprintf("obj-%06d", i), "device", fmt.Sprintf("device-%06d", i), "{}", float64(i%1000), float64(i/1000)); e != nil {
			b.Fatal(e)
		}
	}
	_ = stmt.Close()
	if e = tx.Commit(); e != nil {
		b.Fatal(e)
	}
	a := &App{db: db, sessions: map[string]principal{"bench": {User: "bench", Role: "viewer"}}, sessionExpires: map[string]time.Time{"bench": time.Now().Add(time.Hour)}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/objects?min_x=200&max_x=240&min_y=20&max_y=50&grouping=location&limit=5000", nil)
		r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "bench"})
		w := httptest.NewRecorder()
		a.api(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("viewport API status=%d: %s", w.Code, w.Body.String())
		}
	}
}
