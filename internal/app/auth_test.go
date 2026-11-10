package app

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func certLoginTestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	a, db := editorApp(t, "admin")
	a.loginAttempts = map[string][]time.Time{}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,username,password,role) VALUES('u1','Alice.Admin@example.com','x','admin')`,
		`INSERT INTO users(id,username,password,role) VALUES('u2','gateway-user','x','editor')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return a, db
}

func certLoginRequest(cert *x509.Certificate, verified bool) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/login/certificate", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if verified {
			r.TLS.VerifiedChains = [][]*x509.Certificate{{cert}}
		}
	}
	return r
}

func TestLoginCertificateRequiresTLS(t *testing.T) {
	a, _ := certLoginTestApp(t)
	w := httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(nil, false))
	if w.Code != 401 {
		t.Fatalf("plain HTTP: got %d", w.Code)
	}
	w = httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(&x509.Certificate{Subject: pkix.Name{CommonName: "gateway-user"}}, false))
	if w.Code != 401 {
		t.Fatalf("unverified chain: got %d", w.Code)
	}
}

func TestLoginCertificateEmailAndCommonName(t *testing.T) {
	a, _ := certLoginTestApp(t)
	cert := &x509.Certificate{EmailAddresses: []string{"alice.admin@example.com"}, Subject: pkix.Name{CommonName: "Someone Else"}}
	w := httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(cert, true))
	if w.Code != 200 {
		t.Fatalf("email match: %d %s", w.Code, w.Body.String())
	}
	var p principal
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.User != "Alice.Admin@example.com" || p.Role != "admin" {
		t.Fatalf("email principal: %+v %v", p, err)
	}
	var session string
	for _, c := range w.Result().Cookies() {
		if c.Name == "mapper_session" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("email login set no session cookie")
	}
	w = httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(&x509.Certificate{Subject: pkix.Name{CommonName: "gateway-user"}}, true))
	if w.Code != 200 {
		t.Fatalf("common-name fallback: %d %s", w.Code, w.Body.String())
	}
	// Per-request mTLS identity resolves the same way.
	if got, ok := a.identity(certLoginRequest(cert, true)); !ok || got.User != "Alice.Admin@example.com" {
		t.Fatalf("identity(): %+v %v", got, ok)
	}
}

func TestLoginCertificateUnknownIdentity(t *testing.T) {
	a, _ := certLoginTestApp(t)
	w := httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(&x509.Certificate{EmailAddresses: []string{"stranger@example.com"}}, true))
	if w.Code != 401 || !strings.Contains(w.Body.String(), "stranger@example.com") {
		t.Fatalf("unknown: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.loginCertificate(w, certLoginRequest(&x509.Certificate{}, true))
	if w.Code != 401 {
		t.Fatalf("empty identity: got %d", w.Code)
	}
	locked := &App{loginAttempts: map[string][]time.Time{}}
	w = httptest.NewRecorder()
	locked.loginCertificate(w, certLoginRequest(&x509.Certificate{Subject: pkix.Name{CommonName: "gateway-user"}}, true))
	if w.Code != 423 {
		t.Fatalf("locked: got %d", w.Code)
	}
}

func TestServerInfo(t *testing.T) {
	a, _ := certLoginTestApp(t)
	a.mtlsEnabled = true
	rec := httptest.NewRecorder()
	a.serverInfo(rec, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	var out struct {
		MtTLS  bool `json:"mtls_enabled"`
		Locked bool `json:"locked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.MtTLS || out.Locked {
		t.Fatalf("enabled: %+v %v", out, err)
	}
	locked := &App{}
	rec = httptest.NewRecorder()
	locked.serverInfo(rec, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	out = struct {
		MtTLS  bool `json:"mtls_enabled"`
		Locked bool `json:"locked"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.MtTLS || !out.Locked {
		t.Fatalf("locked: %+v %v", out, err)
	}
}
