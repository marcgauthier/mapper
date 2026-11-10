package app

import (
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// tooManyLoginAttempts records a login attempt and reports whether the peer
// crossed the shared password/certificate attempt budget.
func (a *App) tooManyLoginAttempts(peer string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.loginAttempts[peer][:0]
	for _, t := range a.loginAttempts[peer] {
		if now.Sub(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		a.loginAttempts[peer] = recent
		return true
	}
	a.loginAttempts[peer] = append(recent, now)
	return false
}

func loginPeer(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	return peer
}

// issueSession records a login, creates a twelve-hour session cookie, and
// writes the principal. details carries the login method for the audit trail.
func (a *App) issueSession(w http.ResponseWriter, r *http.Request, p principal, peer string, details []byte) {
	a.logAction(p.User, "login", "", details)
	id := newID()
	a.mu.Lock()
	a.sessions[id] = p
	a.sessionExpires[id] = time.Now().Add(12 * time.Hour)
	delete(a.loginAttempts, peer)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "mapper_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	json.NewEncoder(w).Encode(p)
}

// certificateIdentities returns the usernames a verified client certificate
// may assert: SAN email addresses first, then the subject common name.
func certificateIdentities(cert *x509.Certificate) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	for _, email := range cert.EmailAddresses {
		add(email)
	}
	add(cert.Subject.CommonName)
	return out
}

// principalForCertificate matches a verified certificate against existing
// users without regard to case, since PKI issuers use their own casing.
func principalForCertificate(db *sql.DB, cert *x509.Certificate) (principal, string, bool) {
	ids := certificateIdentities(cert)
	if len(ids) == 0 {
		return principal{}, "", false
	}
	rows, err := db.Query(`SELECT username,role FROM users`)
	if err != nil {
		return principal{}, "", false
	}
	defer rows.Close()
	for rows.Next() {
		var p principal
		if rows.Scan(&p.User, &p.Role) != nil {
			continue
		}
		for _, id := range ids {
			if strings.EqualFold(p.User, id) {
				return p, id, true
			}
		}
	}
	return principal{}, ids[0], false
}

// loginCertificate establishes a session from the verified TLS client
// certificate presented by the browser or its PKI card provider.
func (a *App) loginCertificate(w http.ResponseWriter, r *http.Request) {
	db := a.ready()
	if db == nil {
		http.Error(w, "locked", 423)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peer := loginPeer(r)
	if a.tooManyLoginAttempts(peer) {
		http.Error(w, "too many login attempts", 429)
		return
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "no verified client certificate presented", 401)
		return
	}
	p, id, ok := principalForCertificate(db, r.TLS.PeerCertificates[0])
	if id == "" {
		http.Error(w, "client certificate has no email or common name", 401)
		return
	}
	if !ok {
		http.Error(w, fmt.Sprintf("no account for certificate identity %q", id), 401)
		return
	}
	a.issueSession(w, r, p, peer, []byte(`{"method":"certificate"}`))
}

// serverInfo reports public pre-login facts: whether PKI login can work and
// whether the database is still locked.
func (a *App) serverInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.mu.RLock()
	mtls := a.mtlsEnabled
	a.mu.RUnlock()
	json.NewEncoder(w).Encode(map[string]any{"mtls_enabled": mtls, "locked": a.ready() == nil})
}
