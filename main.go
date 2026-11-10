package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marcgauthier/libmurmur/clients/go/murmurdb"
	"github.com/marcgauthier/loggy"
	"github.com/marcgauthier/websocket"
	"golang.org/x/crypto/bcrypt"
)

type App struct {
	topologyMu     sync.Mutex
	topology       *topologySnapshot
	mu             sync.RWMutex
	storeMu        sync.Mutex
	db             *sql.DB
	logs           *loggy.DB
	auditLogMu     sync.Mutex
	pendingAudit   map[*sql.Tx][]loggy.Entry
	store          string
	clients        map[chan []byte]bool
	sessions       map[string]principal
	sessionExpires map[string]time.Time
	unlockAttempts map[string][]time.Time
	loginAttempts  map[string][]time.Time
}
type principal struct{ User, Role string }

func (a *App) ready() *sql.DB { a.mu.RLock(); defer a.mu.RUnlock(); return a.db }

func main() {
	flag.Parse() // loggy's storage engine requires parsed flags before Open
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := runMapper(cfg); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func runMapper(cfg Config) (result error) {
	a := &App{clients: map[chan []byte]bool{}, sessions: map[string]principal{}, sessionExpires: map[string]time.Time{}, unlockAttempts: map[string][]time.Time{}, loginAttempts: map[string][]time.Time{}}
	a.store = cfg.Store
	actionLogs, err := openActionLogs(cfg.LogDir, cfg.LogRetentionDays)
	if err != nil {
		return fmt.Errorf("open logs: %w", err)
	}
	a.setActionLogs(actionLogs)
	defer actionLogs.Close()
	restoreLogger := a.captureStandardLogs()
	defer restoreLogger()
	defer func() {
		if result != nil {
			a.applicationLog("error", result.Error(), nil)
		}
		a.applicationLog("info", "Mapper stopped", nil)
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/unlock", a.unlock)
	mux.HandleFunc("/restore", a.restore)
	mux.HandleFunc("/logs", a.logsPage)
	mux.HandleFunc("/api/login", a.login)
	mux.HandleFunc("/api/logout", a.logout)
	mux.HandleFunc("/api/me", a.me)
	mux.HandleFunc("/ws", a.ws)
	mux.HandleFunc("/api/", a.api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if a.ready() == nil {
				http.Redirect(w, r, "/unlock", http.StatusSeeOther)
				return
			}
		}
		http.FileServer(http.Dir("web")).ServeHTTP(w, r)
	})
	addr := cfg.Addr
	cert, key := cfg.TLSCert, cfg.TLSKey
	if (cert == "") != (key == "") {
		return fmt.Errorf("tls_cert and tls_key must be set together")
	}
	if cfg.RequireTLS && cert == "" {
		return fmt.Errorf("require_tls requires tls_cert and tls_key")
	}
	tlsConfig, err := clientTLSConfig(cfg.ClientCA)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: a.observeHTTP(zstdHTTP(captureHTTPFailures(mux))), TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ErrorLog: log.New(applicationLogWriter{a, "error"}, "", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan struct{})
	shutdownDone := make(chan struct{})
	defer close(finished)
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				a.applicationLog("error", "HTTP shutdown failed", map[string]string{"error": err.Error()})
			}
		case <-finished:
		}
	}()
	a.applicationLog("info", "Mapper listening", map[string]string{"address": addr})
	if cert != "" {
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		if ctx.Err() != nil {
			<-shutdownDone
		}
		return nil
	}
	return err
}
func clientTLSConfig(caPath string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid client CA")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

func (a *App) unlock(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		http.ServeFile(w, r, "web/unlock.html")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	a.storeMu.Lock()
	defer a.storeMu.Unlock()
	a.mu.RLock()
	alreadyUnlocked := a.db != nil
	a.mu.RUnlock()
	if alreadyUnlocked {
		http.Error(w, "database is already unlocked", http.StatusConflict)
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	now := time.Now()
	if !a.allowUnlockAttempt(peer, now) {
		http.Error(w, "too many unlock attempts; wait five minutes", 429)
		return
	}
	var x struct {
		Key      string `json:"key"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || len(x.Key) < 8 {
		http.Error(w, "database key must contain at least 8 characters", 400)
		return
	}
	sum := databaseKey(x.Key)
	path := a.storePath()
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	db, e := murmurdb.Open(path + "?key=" + hex.EncodeToString(sum[:]) + "&mode=rwc")
	if e != nil {
		http.Error(w, "database unlock failed", 401)
		return
	}
	db.SetMaxOpenConns(1)
	if e = db.Ping(); e != nil {
		_ = db.Close()
		http.Error(w, "database key is incorrect or store is unavailable", http.StatusUnauthorized)
		return
	}
	if e = migrateSchema(db); e != nil {
		db.Close()
		http.Error(w, "database initialization failed", 500)
		return
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
	if count == 0 {
		if len(x.Username) < 3 || len(x.Password) < 12 {
			db.Close()
			http.Error(w, "first unlock requires username and password of at least 12 characters", 400)
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(x.Password), bcrypt.DefaultCost)
		if _, e = db.Exec(`INSERT INTO users(id,username,password,role) VALUES(?,?,?,?)`, newID(), x.Username, string(h), "admin"); e != nil {
			db.Close()
			http.Error(w, "admin setup failed", 500)
			return
		}
	}
	a.mu.Lock()
	if a.db != nil {
		a.mu.Unlock()
		db.Close()
		http.Error(w, "database is already unlocked", http.StatusConflict)
		return
	}
	a.db = db
	delete(a.unlockAttempts, peer)
	a.mu.Unlock()
	w.Write([]byte(`{"success":true}`))
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	db := a.db
	a.mu.RUnlock()
	if db == nil {
		http.Error(w, "locked", 423)
		return
	}
	p, ok := a.identity(r)
	if !ok {
		http.Error(w, "login required", 401)
		return
	}
	switch r.URL.Path {
	case "/api/topology/view", "/api/topology/details", "/api/topology/choices", "/api/topology/cables":
		a.topologyView(w, r, db, p)
		return
	case "/api/topology/attachment":
		a.serviceAttachment(w, r, db, p)
		return
	case "/api/topology/assign-conduit":
		a.assignConduit(w, r, db, p)
		return
	case "/api/relationships/undo":
		a.undoRelationship(w, r, db, p)
		return
	case "/api/relationships":
		a.relationships(w, r, db, p)
		return
	case "/api/topology/convert-service":
		a.convertService(w, r, db, p)
		return
	case "/api/device-visuals":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		json.NewEncoder(w).Encode(deviceVisualCatalog)
		return
	case "/api/networks":
		a.networks(w, r, db, p)
		return
	case "/api/export":
		a.exportCSV(w, r, db)
		return
	case "/api/device-types":
		a.deviceTypes(w, r, db, p)
		return
	case "/api/connection-types":
		a.connectionTypes(w, r, db, p)
		return
	case "/api/backup":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		a.downloadBackup(w, r)
		return
	case "/api/health":
		w.Write([]byte(`{"ready":true}`))
	case "/api/objects":
		if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodGet && p.Role == "viewer" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == http.MethodDelete {
			a.deleteObject(w, r, db, p)
			return
		}
		if r.Method == "GET" {
			limit := 2000
			if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 {
				limit = n
			}
			if limit > 5000 {
				limit = 5000
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			group := r.URL.Query().Get("grouping")
			if group == "" {
				group = "location"
			}
			list := r.URL.Query().Get("ids")
			idField := "o.id"
			containerScope := false
			if hosts := r.URL.Query().Get("host_ids"); hosts != "" {
				list = hosts
				idField = "json_extract(o.data,'$.host_id')"
			}
			if containers := r.URL.Query().Get("container_ids"); containers != "" {
				list = containers
				containerScope = true
			}
			if list != "" {
				ids := strings.Split(list, ",")
				if len(ids) > 500 {
					http.Error(w, "request at most 500 object IDs", 400)
					return
				}
				placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
				args := make([]any, 0, len(ids)+3)
				args = append(args, group)
				where := idField + ` IN (` + placeholders + `)`
				fields := []string{idField}
				if containerScope {
					fields = []string{"json_extract(o.data,'$.site_id')", "json_extract(o.data,'$.building_id')", "json_extract(o.data,'$.telecom_room_id')", "json_extract(o.data,'$.rack_id')"}
					parts := []string{}
					for _, field := range fields {
						parts = append(parts, field+` IN (`+placeholders+`)`)
					}
					where = "(" + strings.Join(parts, " OR ") + ")"
				}
				for range fields {
					for _, id := range ids {
						args = append(args, id)
					}
				}
				args = append(args, limit, offset)
				rows, err := db.Query(`SELECT o.id,o.kind,o.name,o.data,COALESCE(l.x,o.x),COALESCE(l.y,o.y),o.version FROM objects o LEFT JOIN layouts l ON l.object_id=o.id AND l.grouping=? WHERE `+where+` ORDER BY o.id LIMIT ? OFFSET ?`, args...)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()
				out := []map[string]any{}
				for rows.Next() {
					var id, kind, name, data string
					var x, y float64
					var version int
					if rows.Scan(&id, &kind, &name, &data, &x, &y, &version) == nil {
						var metadata map[string]any
						_ = json.Unmarshal([]byte(data), &metadata)
						out = append(out, map[string]any{"id": id, "kind": kind, "name": name, "data": metadata, "x": x, "y": y, "version": version})
					}
				}
				json.NewEncoder(w).Encode(out)
				return
			}
			query := `SELECT id,kind,name,data,x,y,version FROM (
				SELECT o.id,o.kind,o.name,o.data,l.x,l.y,o.version FROM layouts l JOIN objects o ON o.id=l.object_id WHERE l.grouping=? AND (?='' OR o.kind=?) AND (? IS NULL OR l.x>=?) AND (? IS NULL OR l.x<=?) AND (? IS NULL OR l.y>=?) AND (? IS NULL OR l.y<=?)
				UNION ALL
				SELECT o.id,o.kind,o.name,o.data,o.x,o.y,o.version FROM objects o LEFT JOIN layouts l ON l.object_id=o.id AND l.grouping=? WHERE l.object_id IS NULL AND (?='' OR o.kind=?) AND (? IS NULL OR o.x>=?) AND (? IS NULL OR o.x<=?) AND (? IS NULL OR o.y>=?) AND (? IS NULL OR o.y<=?)
			) ORDER BY id LIMIT ? OFFSET ?`
			bound := func(k string) any {
				v := r.URL.Query().Get(k)
				if v == "" {
					return nil
				}
				n, e := strconv.ParseFloat(v, 64)
				if e != nil {
					return nil
				}
				return n
			}
			minx, maxx, miny, maxy := bound("min_x"), bound("max_x"), bound("min_y"), bound("max_y")
			kind := r.URL.Query().Get("kind")
			rows, e := db.Query(query,
				group, kind, kind, minx, minx, maxx, maxx, miny, miny, maxy, maxy,
				group, kind, kind, minx, minx, maxx, maxx, miny, miny, maxy, maxy,
				limit, offset)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			defer rows.Close()
			out := []map[string]any{}
			for rows.Next() {
				var id, k, n, d string
				var x, y float64
				var version int
				if rows.Scan(&id, &k, &n, &d, &x, &y, &version) == nil {
					var v map[string]any
					_ = json.Unmarshal([]byte(d), &v)
					out = append(out, map[string]any{"id": id, "kind": k, "name": n, "data": v, "x": x, "y": y, "version": version})
				}
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			http.Error(w, "invalid json", 400)
			return
		}
		id, _ := v["id"].(string)
		if id == "" {
			id = newID()
		}
		kind, _ := v["kind"].(string)
		name, _ := v["name"].(string)
		if strings.TrimSpace(kind) == "" || strings.TrimSpace(name) == "" {
			http.Error(w, "kind and name are required", 400)
			return
		}
		if kind == "shape" {
			if err := validateShapeSpec(v); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		if kind == "label" {
			if len([]rune(name)) > 2000 {
				http.Error(w, "label text must be at most 2000 characters", 400)
				return
			}
			if err := validateLabelSpec(v); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		v["id"], v["kind"], v["name"] = id, kind, name
		if _, ok := v["x"]; !ok {
			v["x"] = float64(0)
		}
		if _, ok := v["y"]; !ok {
			v["y"] = float64(0)
		}
		if _, ok := v["x"].(float64); !ok {
			http.Error(w, "x and y coordinates must be numbers", 400)
			return
		}
		if _, ok := v["y"].(float64); !ok {
			http.Error(w, "x and y coordinates must be numbers", 400)
			return
		}
		tx, e := db.Begin()
		if e != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		if err := normalizeTopologyPlacement(tx, id, kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := normalizeLocationParent(tx, kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := normalizeRackPlacement(tx, kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := normalizeConduitEndpoints(tx, kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := requirePlacement(kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if typeID, _ := v["device_type_id"].(string); typeID != "" {
			var typeKind, typeName, visual, medium string
			if e = tx.QueryRow(`SELECT object_kind,name,visual,medium FROM device_types WHERE id=? AND enabled=1`, typeID).Scan(&typeKind, &typeName, &visual, &medium); e != nil || typeKind != kind {
				http.Error(w, "selected network item type does not match the item category", http.StatusBadRequest)
				return
			}
			v["device_type"] = typeName
			v["device_visual"] = visual
			v["medium"] = medium
		} else if hostedKind(kind) {
			http.Error(w, "hosted item requires a catalog device type", http.StatusBadRequest)
			return
		}
		if err := normalizeDeviceVRFs(tx, id, kind, v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if hostedKind(kind) {
			if err := prepareHostedObject(tx, id, kind, v); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		if kind == "device" {
			var virtualMachines int
			if e = tx.QueryRow(`SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?`, id).Scan(&virtualMachines); e != nil {
				http.Error(w, "hypervisor child reference check failed", 500)
				return
			}
			if virtualMachines > 0 && !isHypervisorMeta(v) {
				http.Error(w, "move or remove hosted VMs and vSwitches before changing this hypervisor type", http.StatusConflict)
				return
			}
		}
		hostingSource := topologyText(v, "hosting_source_id")
		delete(v, "hosting_source_id")
		data, _ := json.Marshal(v)
		var before string
		var currentVersion int
		var currentKind string
		lookupErr := tx.QueryRow(`SELECT data,version,kind FROM objects WHERE id=?`, id).Scan(&before, &currentVersion, &currentKind)
		if lookupErr == nil && v["version"] != nil {
			expected, ok := v["version"].(float64)
			if !ok || int(expected) != currentVersion {
				http.Error(w, "object changed since it was loaded; refresh and retry", http.StatusConflict)
				return
			}
		}
		if lookupErr == nil && currentKind != kind {
			var refs int
			if e = tx.QueryRow(`SELECT (SELECT count(*) FROM interfaces WHERE device_id=?) + (SELECT count(*) FROM cable_routes WHERE infrastructure_id=?) + (SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?)`, id, id, id).Scan(&refs); e != nil {
				http.Error(w, "object reference check failed", 500)
				return
			}
			if refs == 0 {
				if n, err := locationChildCount(tx, id); err != nil {
					http.Error(w, "location reference check failed", 500)
					return
				} else {
					refs = n
				}
			}
			if refs != 0 {
				http.Error(w, "cannot change the type of an object with interfaces or cable route references", 409)
				return
			}
		}
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			http.Error(w, "object lookup failed", 500)
			return
		}
		_, e = tx.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,name=excluded.name,data=excluded.data,x=excluded.x,y=excluded.y,version=objects.version+1`, id, kind, name, string(data), v["x"], v["y"])
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if kind == "building" && lookupErr == sql.ErrNoRows {
			if err := ensureGround(tx, id); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		if err := ensureTopologyPorts(tx, id, kind, v); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if lookupErr == nil {
			if err := refreshTopologyDescendants(tx, id, kind); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}

		if hostingSource != "" && lookupErr == sql.ErrNoRows {
			relation := topologyRelationship{ID: newID(), Source: hostingSource, Target: id, Type: "hosts", Version: 1}
			if err := validateRelationship(tx, relation); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if _, err := tx.Exec(`INSERT INTO relationships(id,source_id,target_id,relationship_type,version,label) VALUES(?,?,?,'hosts',1,'')`, relation.ID, hostingSource, id); err != nil {
				http.Error(w, "hosting relation failed", 500)
				return
			}
		}
		createdInterfaces := []map[string]any{}
		if kind == "shape" {
			if err := ensureShapeInterface(tx, id); err != nil {
				http.Error(w, "shape connection point creation failed", 500)
				return
			}
			if lookupErr == sql.ErrNoRows {
				var point string
				if err := tx.QueryRow(`SELECT id FROM interfaces WHERE device_id=?`, id).Scan(&point); err != nil {
					http.Error(w, "shape point lookup failed", 500)
					return
				}
				createdInterfaces = append(createdInterfaces, map[string]any{"id": point, "device_id": id, "name": "Connection point", "data": map[string]any{}})
			}
		}
		if lookupErr == sql.ErrNoRows && (kind == "device" || hostedKind(kind)) {
			interfaceNames := []string{"Interface 1"}
			interfaceRoles := map[string]string{}
			if kind == "virtual_machine" {
				interfaceNames = []string{"eth0"}
			} else if kind == "virtual_switch" {
				interfaceNames = []string{"Port 1"}
			} else if kind == "device" {
				if typeID, _ := v["device_type_id"].(string); typeID != "" {
					if roles, ok := sidedDeviceInterfaces[typeID]; ok {
						interfaceNames = roles
						for _, role := range roles {
							interfaceRoles[role] = role
						}
					}
				}
			}
			for _, interfaceName := range interfaceNames {
				interfaceID := newID()
				interfaceData := []byte(`{}`)
				if role, ok := interfaceRoles[interfaceName]; ok {
					interfaceData, _ = json.Marshal(map[string]string{"role": role})
				}
				if _, e = tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?)`, interfaceID, id, interfaceName, string(interfaceData)); e != nil {
					http.Error(w, "default interface creation failed", 500)
					return
				}
				createdInterfaces = append(createdInterfaces, map[string]any{"id": interfaceID, "device_id": id, "name": interfaceName, "data": json.RawMessage(interfaceData)})
			}
		}
		if lookupErr == sql.ErrNoRows && (kind == "outlet" || kind == "service") {
			rows, err := tx.Query(`SELECT id,name,data FROM interfaces WHERE device_id=?`, id)
			if err != nil {
				http.Error(w, "port snapshot failed", 500)
				return
			}
			for rows.Next() {
				var port, name, raw string
				if err = rows.Scan(&port, &name, &raw); err != nil {
					rows.Close()
					http.Error(w, "port snapshot failed", 500)
					return
				}
				createdInterfaces = append(createdInterfaces, map[string]any{"id": port, "device_id": id, "name": name, "data": json.RawMessage(raw)})
			}
			rows.Close()
		}
		budgetChanges, err := reconcileInterfaceBudgets(tx, id, v)
		if err != nil {
			http.Error(w, "interface allocation update failed", 500)
			return
		}
		dependentStamp, err := topologyDependentStamp(tx, id, kind)
		if err != nil {
			http.Error(w, "dependent snapshot failed", 500)
			return
		}
		var beforeSnapshot any
		if lookupErr == nil {
			beforeSnapshot = json.RawMessage(before)
		}
		if e = a.recordAudit(tx, p.User, "object_upsert", id, map[string]any{"before": beforeSnapshot, "after": json.RawMessage(data), "created_interfaces": createdInterfaces, "dependent_stamp": dependentStamp, "interface_budget_changes": budgetChanges}); e != nil {
			http.Error(w, "audit write failed", 500)
			return
		}
		seq, e := appendEvent(tx, data)
		if e != nil {
			http.Error(w, "event write failed", 500)
			return
		}
		if e = a.commitLogged(tx); e != nil {
			http.Error(w, "commit failed", 500)
			return
		}
		a.notify(seq, data)
		w.WriteHeader(204)
	case "/api/interfaces":
		a.interfaces(w, r, db, p)
		return
	case "/api/links":
		a.links(w, r, db, p)
		return
	case "/api/search":
		a.search(w, r, db)
		return
	case "/api/audit":
		a.audit(w, r, db)
		return
	case "/api/client-errors":
		a.clientError(w, r, p)
		return
	case "/api/logs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("category") == "system" && p.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		a.queryLogs(w, r)
		return
	case "/api/undo":
		a.undo(w, r, db, p)
		return
	case "/api/events":
		a.events(w, r, db)
		return
	case "/api/users":
		a.users(w, r, db, p)
		return
	case "/api/import":
		a.csvImport(w, r, db, p)
		return
	case "/api/pairings":
		a.pairings(w, r, db, p)
		return
	case "/api/routes":
		a.routes(w, r, db, p)
		return
	case "/api/trace":
		a.trace(w, r, db)
		return
	case "/api/layout":
		a.layout(w, r, db, p)
		return
	default:
		if strings.HasPrefix(r.URL.Path, "/api/search") {
			q := "%" + r.URL.Query().Get("q") + "%"
			rows, _ := db.Query(`SELECT id,kind,name FROM objects WHERE name LIKE ? LIMIT 100`, q)
			defer rows.Close()
			out := []map[string]string{}
			for rows.Next() {
				var i, k, n string
				rows.Scan(&i, &k, &n)
				out = append(out, map[string]string{"id": i, "kind": k, "name": n})
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		http.NotFound(w, r)
	}
}

func (a *App) deleteObject(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", 400)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var kind, name, data string
	var x, y float64
	if err = tx.QueryRow(`SELECT kind,name,data,x,y FROM objects WHERE id=?`, id).Scan(&kind, &name, &data, &x, &y); err != nil {
		http.Error(w, "object not found", 404)
		return
	}
	var referenced int
	if err = tx.QueryRow(`SELECT count(*) FROM interfaces i LEFT JOIN links l ON l.source_interface=i.id OR l.target_interface=i.id LEFT JOIN patch_pairings p ON p.port_a=i.id OR p.port_b=i.id WHERE i.device_id=? AND (l.id IS NOT NULL OR p.id IS NOT NULL)`, id).Scan(&referenced); err != nil {
		http.Error(w, "reference check failed", 500)
		return
	}
	if referenced != 0 {
		http.Error(w, "remove connected cables and panel pairings before deleting this object", 409)
		return
	}
	if err = tx.QueryRow(`SELECT count(*) FROM cable_routes WHERE infrastructure_id=?`, id).Scan(&referenced); err != nil {
		http.Error(w, "route reference check failed", 500)
		return
	}
	if referenced != 0 {
		http.Error(w, "remove this object from cable routes before deleting it", 409)
		return
	}
	if err = tx.QueryRow(`SELECT count(*) FROM objects WHERE kind='conduit' AND (json_extract(data,'$.endpoint_a_id')=? OR json_extract(data,'$.endpoint_b_id')=?)`, id, id).Scan(&referenced); err != nil {
		http.Error(w, "conduit endpoint reference check failed", 500)
		return
	}
	if referenced != 0 {
		http.Error(w, "update conduits that use this object as an endpoint before deleting it", 409)
		return
	}
	if kind == "device" {
		if err = tx.QueryRow(`SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?`, id).Scan(&referenced); err != nil {
			http.Error(w, "hosted hypervisor child reference check failed", 500)
			return
		}
		if referenced != 0 {
			http.Error(w, "move or delete hosted VMs and vSwitches before deleting this hypervisor", 409)
			return
		}
	}
	if err = topologyDeleteGuard(tx, id, kind); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	field := map[string]string{"site": "site_id", "building": "building_id", "floor": "floor_id", "room": "room_id", "telecom_room": "telecom_room_id", "rack": "rack_id"}[kind]
	if field != "" {
		query := `SELECT count(*) FROM objects WHERE id<>? AND (json_extract(data,'$.` + field + `')=? OR json_extract(data,'$.` + field + `')=?)`
		if err = tx.QueryRow(query, id, id, name).Scan(&referenced); err != nil {
			http.Error(w, "location reference check failed", 500)
			return
		}
		if referenced != 0 {
			http.Error(w, "update objects that reference this location before deleting it", 409)
			return
		}
	}
	if _, err = tx.Exec(`DELETE FROM interfaces WHERE device_id=?`, id); err == nil {
		_, err = tx.Exec(`DELETE FROM layouts WHERE object_id=?`, id)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM objects WHERE id=?`, id)
	}
	if err != nil {
		http.Error(w, "object delete failed", 500)
		return
	}
	if err = a.recordAudit(tx, p.User, "object_delete", id, map[string]any{"before": map[string]any{"id": id, "kind": kind, "name": name, "data": json.RawMessage(data), "x": x, "y": y}}); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "object_delete", "id": id})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "delete commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) deleteInterface(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", 400)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var device, name, data string
	if err = tx.QueryRow(`SELECT device_id,name,data FROM interfaces WHERE id=?`, id).Scan(&device, &name, &data); err != nil {
		http.Error(w, "interface not found", 404)
		return
	}
	var refs int
	if err = tx.QueryRow(`SELECT (SELECT count(*) FROM links WHERE source_interface=? OR target_interface=?) + (SELECT count(*) FROM patch_pairings WHERE port_a=? OR port_b=?)`, id, id, id, id).Scan(&refs); err != nil {
		http.Error(w, "reference check failed", 500)
		return
	}
	if refs != 0 {
		http.Error(w, "disconnect and unpair this interface before deleting it", 409)
		return
	}
	var ownerKind string
	if err = tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, device).Scan(&ownerKind); err != nil {
		http.Error(w, "interface owner not found", 404)
		return
	}
	if ownerKind == "shape" {
		http.Error(w, "a shape must retain its shared connection point", 409)
		return
	}
	if ownerKind == "device" {
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=?`, device).Scan(&count); err != nil {
			http.Error(w, "interface count check failed", 500)
			return
		}
		if count <= 1 {
			http.Error(w, "a device must retain at least one interface", 409)
			return
		}
	}
	if _, err = tx.Exec(`DELETE FROM interfaces WHERE id=?`, id); err != nil {
		http.Error(w, "interface delete failed", 500)
		return
	}
	if err = a.recordAudit(tx, p.User, "interface_delete", id, map[string]any{"device_id": device, "name": name, "data": json.RawMessage(data)}); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "interface_delete", "id": id})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "delete commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) deleteLink(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", 400)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var source, target, data string
	if err = tx.QueryRow(`SELECT source_interface,target_interface,data FROM links WHERE id=?`, id).Scan(&source, &target, &data); err != nil {
		http.Error(w, "link not found", 404)
		return
	}
	var route []string
	rows, err := tx.Query(`SELECT infrastructure_id FROM cable_routes WHERE link_id=? ORDER BY sequence`, id)
	if err != nil {
		http.Error(w, "route lookup failed", 500)
		return
	}
	for rows.Next() {
		var item string
		if rows.Scan(&item) == nil {
			route = append(route, item)
		}
	}
	rows.Close()
	if _, err = tx.Exec(`DELETE FROM cable_routes WHERE link_id=?`, id); err == nil {
		_, err = tx.Exec(`DELETE FROM links WHERE id=?`, id)
	}
	if err != nil {
		http.Error(w, "link delete failed", 500)
		return
	}
	if err = a.recordAudit(tx, p.User, "link_delete", id, map[string]any{"source_interface": source, "target_interface": target, "data": json.RawMessage(data), "route": route}); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "link_delete", "id": id})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "delete commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) ws(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.identity(r); !ok {
		http.Error(w, "login required", 401)
		return
	}
	supported := false
	for _, protocol := range websocket.Subprotocols(r) {
		if protocol == zstdWebSocketProtocol {
			supported = true
		}
	}
	if !supported {
		http.Error(w, "WebSocket requires mapper.zstd.v1", http.StatusUpgradeRequired)
		return
	}
	ch := make(chan []byte, 64)
	a.mu.Lock()
	a.clients[ch] = true
	a.mu.Unlock()
	u := websocket.Upgrader{Subprotocols: []string{zstdWebSocketProtocol}, CheckOrigin: func(req *http.Request) bool {
		origin := req.Header.Get("Origin")
		if origin == "" {
			return true
		}
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		return origin == scheme+"://"+req.Host
	}}
	c, e := u.Upgrade(w, r, nil)
	if e != nil {
		a.mu.Lock()
		delete(a.clients, ch)
		a.mu.Unlock()
		return
	}
	a.applicationLog("info", "WebSocket connected", nil)
	defer a.applicationLog("info", "WebSocket disconnected", nil)
	c.SetReadLimit(maxWSDecodedBody)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			kind, frame, err := c.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.BinaryMessage {
				a.applicationLog("warn", "WebSocket rejected uncompressed message", nil)
				_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "Zstandard binary messages required"), time.Now().Add(time.Second))
				return
			}
			decoded, err := decodeZstdMessage(frame)
			if err != nil || !json.Valid(decoded) {
				a.applicationLog("warn", "WebSocket rejected invalid Zstandard JSON", nil)
				_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "invalid Zstandard JSON message"), time.Now().Add(time.Second))
				return
			}
		}
	}()
	for {
		select {
		case b := <-ch:
			if e := c.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil {
				goto end
			}
			if e := c.WriteMessage(websocket.BinaryMessage, b); e != nil {
				a.applicationLog("warn", "WebSocket send failed", map[string]string{"error": e.Error()})
				goto end
			}
		case <-done:
			goto end
		}
	}
end:
	a.mu.Lock()
	delete(a.clients, ch)
	a.mu.Unlock()
	c.Close()
}

func newID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func (a *App) identity(r *http.Request) (principal, bool) {
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.PeerCertificates) > 0 {
		db := a.ready()
		if db != nil {
			cn := r.TLS.PeerCertificates[0].Subject.CommonName
			var p principal
			if db.QueryRow(`SELECT username,role FROM users WHERE username=?`, cn).Scan(&p.User, &p.Role) == nil {
				return p, true
			}
		}
	}
	c, e := r.Cookie("mapper_session")
	if e != nil {
		return principal{}, false
	}
	a.mu.RLock()
	p, ok := a.sessions[c.Value]
	expires := a.sessionExpires[c.Value]
	a.mu.RUnlock()
	if ok && time.Now().After(expires) {
		a.mu.Lock()
		if current, exists := a.sessionExpires[c.Value]; exists && time.Now().After(current) {
			delete(a.sessions, c.Value)
			delete(a.sessionExpires, c.Value)
		}
		a.mu.Unlock()
		return principal{}, false
	}
	return p, ok
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	db := a.ready()
	if db == nil {
		http.Error(w, "locked", 423)
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	now := time.Now()
	a.mu.Lock()
	recent := a.loginAttempts[peer][:0]
	for _, t := range a.loginAttempts[peer] {
		if now.Sub(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		a.mu.Unlock()
		http.Error(w, "too many login attempts", 429)
		return
	}
	a.loginAttempts[peer] = append(recent, now)
	a.mu.Unlock()
	var x struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		http.Error(w, "invalid", 400)
		return
	}
	var p principal
	var stored string
	e := db.QueryRow(`SELECT username,role,password FROM users WHERE username=?`, x.Username).Scan(&p.User, &p.Role, &stored)
	if e == nil && bcrypt.CompareHashAndPassword([]byte(stored), []byte(x.Password)) != nil {
		e = fmt.Errorf("invalid credentials")
	}
	if e != nil {
		http.Error(w, "invalid credentials", 401)
		return
	}
	a.logAction(p.User, "login", "", []byte(`{}`))
	id := newID()
	a.mu.Lock()
	a.sessions[id] = p
	a.sessionExpires[id] = time.Now().Add(12 * time.Hour)
	delete(a.loginAttempts, peer)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "mapper_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	json.NewEncoder(w).Encode(p)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if p, ok := a.identity(r); ok {
		a.logAction(p.User, "logout", "", []byte(`{}`))
	}
	if c, e := r.Cookie("mapper_session"); e == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		delete(a.sessionExpires, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "mapper_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(204)
}
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p, ok := a.identity(r)
	if !ok {
		http.Error(w, "login required", 401)
		return
	}
	json.NewEncoder(w).Encode(p)
}
func (a *App) interfaces(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == http.MethodGet {
		a.listInterfaces(w, r, db)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", 403)
		return
	}
	if r.Method == http.MethodDelete {
		a.deleteInterface(w, r, db, p)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var x struct {
		ExpectedRevision string `json:"expected_revision"`
		ID               string `json:"id"`
		DeviceID         string `json:"device_id"`
		Name             string `json:"name"`
		Data             any    `json:"data"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if decoder.Decode(&x) != nil || strings.TrimSpace(x.DeviceID) == "" || strings.TrimSpace(x.Name) == "" {
		http.Error(w, "invalid", 400)
		return
	}
	if x.ID == "" {
		x.ID = newID()
	}
	var ownerKind string
	if err := db.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.DeviceID).Scan(&ownerKind); err != nil || !interfaceOwnerKindAllowed(ownerKind) {
		http.Error(w, "device_id must reference a device, VM, patch panel, shape, text label, or manhole", http.StatusBadRequest)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var ownerRaw string
	if err := tx.QueryRow(`SELECT data FROM objects WHERE id=?`, x.DeviceID).Scan(&ownerRaw); err != nil {
		http.Error(w, "interface owner was removed", 409)
		return
	}
	var before any
	var oldDevice, oldName, oldData string
	err := tx.QueryRow(`SELECT device_id,name,data FROM interfaces WHERE id=?`, x.ID).Scan(&oldDevice, &oldName, &oldData)
	if err == nil {
		if oldDevice != x.DeviceID {
			http.Error(w, "an existing interface cannot be moved to another device", 409)
			return
		}
		if x.ExpectedRevision != "" && x.ExpectedRevision != interfaceRevision(x.ID, oldDevice, oldName, oldData, ownerRaw) {
			http.Error(w, "interface or device changed; refresh before saving", 409)
			return
		}
		before = map[string]any{"id": x.ID, "device_id": oldDevice, "name": oldName, "data": json.RawMessage(oldData)}
	} else if err == sql.ErrNoRows && x.ExpectedRevision != "" {
		http.Error(w, "interface was removed; refresh before saving", 409)
		return
	} else if err != sql.ErrNoRows {
		http.Error(w, "interface lookup failed", 500)
		return
	}
	var owner map[string]any
	_ = json.Unmarshal([]byte(ownerRaw), &owner)
	meta, ok := x.Data.(map[string]any)
	if x.Data == nil {
		meta = map[string]any{}
		ok = true
	}
	if !ok {
		http.Error(w, "interface data must be an object", 400)
		return
	}
	if oldData != "" {
		previous, _ := decodeInterfaceMetadata(oldData)
		oldBudgets, _ := interfaceBudgetMap(previous["vrf_bandwidth_bps"])
		budgets, err := interfaceBudgetMap(meta["vrf_bandwidth_bps"])
		if err == nil && len(budgets) > 0 {
			carried := map[string]bool{}
			for _, name := range effectiveInterfaceVRFs(owner, meta) {
				carried[name] = true
			}
			oldCarried := map[string]bool{}
			for _, name := range effectiveInterfaceVRFs(owner, previous) {
				oldCarried[name] = true
			}
			for name := range budgets {
				if !carried[name] && oldCarried[name] && oldBudgets[name] > 0 {
					delete(budgets, name)
				}
			}
			if meta["interface_usage"] == "regular" {
				for name, rate := range budgets {
					if oldBudgets[name] == rate {
						delete(budgets, name)
					}
				}
			}
			meta["vrf_bandwidth_bps"] = budgets
		}
	}
	if err := normalizeInterfaceVRFs(owner, meta); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, err := normalizeInterfaceCapacity(ownerKind, owner, meta, true); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	x.Data = meta
	b, _ := json.Marshal(meta)
	if ownerKind == "shape" {
		if err := checkShapeInterface(tx, x.DeviceID, x.ID); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
	}
	_, e = tx.Exec(`INSERT INTO interfaces VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,data=excluded.data`, x.ID, x.DeviceID, x.Name, string(b))
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if e = a.recordAudit(tx, p.User, "interface_upsert", x.ID, map[string]any{"before": before, "after": map[string]any{"id": x.ID, "device_id": x.DeviceID, "name": x.Name, "data": x.Data}}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	seq, e := appendEvent(tx, b)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, b)
	w.WriteHeader(204)
}
func (a *App) links(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == "GET" {
		limit := 2000
		if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 {
			limit = n
		}
		if limit > 10000 {
			limit = 10000
		}
		bound := func(k string) any {
			v := r.URL.Query().Get(k)
			if v == "" {
				return nil
			}
			n, e := strconv.ParseFloat(v, 64)
			if e != nil {
				return nil
			}
			return n
		}
		minx, maxx, miny, maxy := bound("min_x"), bound("max_x"), bound("min_y"), bound("max_y")
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		group := r.URL.Query().Get("grouping")
		if group == "" {
			group = "location"
		}
		base := `SELECT l.id,l.source_interface,l.target_interface,l.data FROM links l JOIN interfaces si ON si.id=l.source_interface JOIN interfaces ti ON ti.id=l.target_interface JOIN objects so ON so.id=si.device_id JOIN objects tobj ON tobj.id=ti.device_id LEFT JOIN layouts sl ON sl.object_id=so.id AND sl.grouping=? LEFT JOIN layouts tl ON tl.object_id=tobj.id AND tl.grouping=?`
		query := base + ` ORDER BY l.id LIMIT ? OFFSET ?`
		args := []any{group, group, limit, offset}
		if minx != nil && maxx != nil && miny != nil && maxy != nil {
			query = base + ` WHERE ((COALESCE(sl.x,so.x)>=? AND COALESCE(sl.x,so.x)<=? AND COALESCE(sl.y,so.y)>=? AND COALESCE(sl.y,so.y)<=?) OR (COALESCE(tl.x,tobj.x)>=? AND COALESCE(tl.x,tobj.x)<=? AND COALESCE(tl.y,tobj.y)>=? AND COALESCE(tl.y,tobj.y)<=?) OR EXISTS (SELECT 1 FROM cable_routes cr JOIN objects ro ON ro.id=cr.infrastructure_id LEFT JOIN layouts rl ON rl.object_id=ro.id AND rl.grouping=? WHERE cr.link_id=l.id AND COALESCE(rl.x,ro.x)>=? AND COALESCE(rl.x,ro.x)<=? AND COALESCE(rl.y,ro.y)>=? AND COALESCE(rl.y,ro.y)<=?)) ORDER BY l.id LIMIT ? OFFSET ?`
			args = []any{group, group, minx, maxx, miny, maxy, minx, maxx, miny, maxy, group, minx, maxx, miny, maxy, limit, offset}
		}
		if values := r.URL.Query().Get("device_ids"); values != "" {
			ids := strings.Split(values, ",")
			if len(ids) > 500 {
				http.Error(w, "request at most 500 device IDs", 400)
				return
			}
			marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
			query = base + ` WHERE si.device_id IN (` + marks + `) OR ti.device_id IN (` + marks + `) ORDER BY l.id LIMIT ? OFFSET ?`
			args = []any{group, group}
			for _, id := range ids {
				args = append(args, id)
			}
			for _, id := range ids {
				args = append(args, id)
			}
			args = append(args, limit, offset)
		}
		rows, e := db.Query(query, args...)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, s, t, d string
			rows.Scan(&id, &s, &t, &d)
			out = append(out, map[string]string{"id": id, "source_interface": s, "target_interface": t, "data": d})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", 403)
		return
	}
	if r.Method == http.MethodDelete {
		a.deleteLink(w, r, db, p)
		return
	}
	if r.Method == http.MethodPut {
		a.updateLink(w, r, db, p)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var x struct {
		ID      string `json:"id"`
		Source  string `json:"source_interface"`
		Target  string `json:"target_interface"`
		Data    any    `json:"data"`
		Conduit string `json:"conduit_id"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.Source == "" || x.Target == "" || x.Source == x.Target {
		http.Error(w, "link requires two distinct interface ids", 400)
		return
	}
	spec, specErr := validateLinkSpecWithDB(x.Data, db)
	if specErr != nil {
		http.Error(w, specErr.Error(), 400)
		return
	}
	var sd, td string
	if db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, x.Source).Scan(&sd) != nil || db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, x.Target).Scan(&td) != nil || (sd == td && spec["connection_class"] == "physical") {
		http.Error(w, "interfaces must belong to different devices", 400)
		return
	}
	if x.ID == "" {
		x.ID = newID()
	}
	b, _ := json.Marshal(spec)
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	if err := checkLinkAvailability(tx, x.Source, x.Target, spec, ""); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	_, e = tx.Exec(`INSERT INTO links VALUES(?,?,?,?)`, x.ID, x.Source, x.Target, string(b))
	if e != nil {
		http.Error(w, "interface already connected or invalid link", 409)
		return
	}

	if x.Conduit != "" {
		var kind string
		if spec["connection_class"] != "physical" || tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.Conduit).Scan(&kind) != nil || kind != "conduit" {
			http.Error(w, "route requires an existing conduit and physical cable", 400)
			return
		}
		if _, e = tx.Exec(`INSERT INTO cable_routes VALUES(?,?,0,?)`, newID(), x.ID, x.Conduit); e != nil {
			http.Error(w, "conduit route save failed", 500)
			return
		}
	}

	if e = a.recordAudit(tx, p.User, "link_create", x.ID, map[string]any{"id": x.ID, "source_interface": x.Source, "target_interface": x.Target, "data": spec}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	seq, e := appendEvent(tx, b)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, b)
	w.WriteHeader(204)
}

// updateLink edits a connection's endpoints and spec. Availability is checked
// excluding the link itself so unchanged endpoints never conflict. Attached
// cable routes are preserved.
func (a *App) updateLink(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	var x struct {
		ID           string  `json:"id"`
		Source       string  `json:"source_interface"`
		Target       string  `json:"target_interface"`
		Data         any     `json:"data"`
		ExpectedData *string `json:"expected_data"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.ID == "" || x.Source == "" || x.Target == "" || x.Source == x.Target {
		http.Error(w, "link update requires an id and two distinct interface ids", 400)
		return
	}
	var beforeSource, beforeTarget, beforeData string
	if db.QueryRow(`SELECT source_interface,target_interface,data FROM links WHERE id=?`, x.ID).Scan(&beforeSource, &beforeTarget, &beforeData) != nil {
		http.Error(w, "link not found", 404)
		return
	}
	spec, specErr := validateLinkSpecWithDB(x.Data, db)
	if specErr != nil {
		http.Error(w, specErr.Error(), 400)
		return
	}
	var sd, td string
	if db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, x.Source).Scan(&sd) != nil || db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, x.Target).Scan(&td) != nil || (sd == td && spec["connection_class"] == "physical") {
		http.Error(w, "interfaces must belong to different devices", 400)
		return
	}
	b, _ := json.Marshal(spec)
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	if x.ExpectedData != nil {
		var current string
		if tx.QueryRow(`SELECT data FROM links WHERE id=?`, x.ID).Scan(&current) != nil {
			http.Error(w, "link missing", 404)
			return
		}
		var expectedValue, currentValue any
		_ = json.Unmarshal([]byte(*x.ExpectedData), &expectedValue)
		_ = json.Unmarshal([]byte(current), &currentValue)
		expectedJSON, _ := json.Marshal(expectedValue)
		currentJSON, _ := json.Marshal(currentValue)
		if string(expectedJSON) != string(currentJSON) {
			http.Error(w, "connection changed; reload before saving", 409)
			return
		}
	}

	if err := checkLinkAvailability(tx, x.Source, x.Target, spec, x.ID); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if _, e = tx.Exec(`UPDATE links SET source_interface=?,target_interface=?,data=? WHERE id=?`, x.Source, x.Target, string(b), x.ID); e != nil {
		http.Error(w, "link update failed", 500)
		return
	}

	after := map[string]any{"id": x.ID, "source_interface": x.Source, "target_interface": x.Target, "data": spec}
	if e = a.recordAudit(tx, p.User, "link_update", x.ID, map[string]any{"before": map[string]any{"id": x.ID, "source_interface": beforeSource, "target_interface": beforeTarget, "data": json.RawMessage(beforeData)}, "after": after}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "link_update", "id": x.ID})
	seq, e := appendEvent(tx, event)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}

func validateLinkSpec(value any) (map[string]any, error) {
	return validateLinkSpecWithCustoms(value, nil)
}

func validateLinkSpecWithDB(value any, db *sql.DB) (map[string]any, error) {
	return validateLinkSpecWithCustoms(value, func(class, id string) bool {
		return customConnectionSubtype(db, class, id)
	})
}

func validateLinkSpecWithCustoms(value any, custom func(class, id string) bool) (map[string]any, error) {
	spec, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("connection requires physical, virtual, tunnel, or provider service details")
	}
	text := func(key string) string { v, _ := spec[key].(string); return strings.TrimSpace(v) }
	class := strings.ToLower(text("connection_class"))
	if class == "" {
		class = "physical" // Existing saved links only contain medium/cable fields.
	}
	clean := map[string]any{"connection_class": class}
	switch class {
	case "physical":
		physicalType := strings.ToLower(text("physical_type"))
		if physicalType == "" {
			physicalType = strings.ToLower(text("medium"))
		}
		if physicalType == "fibre" || physicalType == "optical" {
			physicalType = "fiber"
		}
		if physicalType == "copper" || physicalType == "twisted_pair" {
			physicalType = "copper_twisted_pair"
		}
		valid := map[string]bool{"fiber": true, "copper_twisted_pair": true, "coax": true, "twinax_dac": true, "wireless_radio": true, "backplane": true, "other": true}
		isCustom := custom != nil && custom("physical", physicalType)
		if !valid[physicalType] && !isCustom {
			return nil, fmt.Errorf("physical_type must be fiber, copper_twisted_pair, coax, twinax_dac, wireless_radio, backplane, other, or a custom physical type")
		}
		medium := physicalType
		if physicalType == "copper_twisted_pair" {
			medium = "copper" // Preserve the legacy medium value used by older clients.
		}
		clean["medium"], clean["physical_type"] = medium, physicalType
		cableType, connectorA, connectorB := text("cable_type"), text("connector_a"), text("connector_b")
		needsCableDetails := physicalType != "wireless_radio" && physicalType != "backplane"
		if needsCableDetails && (cableType == "" || connectorA == "" || connectorB == "") {
			return nil, fmt.Errorf("cable_type and both connectors are required for this physical medium")
		}
		for key, value := range map[string]string{"cable_type": cableType, "connector_a": connectorA, "connector_b": connectorB} {
			if value != "" {
				clean[key] = value
			}
		}
		if value, exists := spec["strand_count"]; exists {
			n, err := strconv.Atoi(fmt.Sprint(value))
			if err != nil || n < 1 {
				return nil, fmt.Errorf("strand_count must be a positive whole number")
			}
			clean["strand_count"] = n
		}
		if value, exists := spec["length_m"]; exists {
			n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return nil, fmt.Errorf("length_m must be a nonnegative number")
			}
			clean["length_m"] = n
		}
		for _, key := range []string{"part_number", "shielding"} {
			if v := text(key); v != "" {
				clean[key] = v
			}
		}
	case "virtual":
		kind := strings.ToLower(text("virtual_type"))
		valid := map[string]bool{"ethernet": true, "vlan": true, "bridge": true, "veth": true, "port_group": true, "overlay": true, "bond": true, "lag": true, "loopback": true, "other": true}
		if !valid[kind] && (custom == nil || !custom("virtual", kind)) {
			return nil, fmt.Errorf("virtual_type must be ethernet, vlan, bridge, veth, port_group, overlay, bond, lag, loopback, other, or a custom virtual type")
		}
		clean["virtual_type"] = kind
		for key, max := range map[string]int{"vlan_id": 4094, "vni": 16777215} {
			if value, exists := spec[key]; exists {
				n, err := strconv.Atoi(fmt.Sprint(value))
				if err != nil || n < 1 || n > max {
					return nil, fmt.Errorf("%s must be a whole number between 1 and %d", key, max)
				}
				clean[key] = n
			}
		}
		for _, key := range []string{"network_name", "bandwidth"} {
			if v := text(key); v != "" {
				clean[key] = v
			}
		}
	case "logical_tunnel":
		protocol := strings.ToLower(text("tunnel_protocol"))
		protocols := map[string]string{"ipsec": "IPsec", "wireguard": "WireGuard", "gre": "GRE", "vxlan": "VXLAN", "l2tp": "L2TP", "geneve": "Geneve", "ipip": "IPIP", "sit": "SIT", "openvpn": "OpenVPN", "ssl_vpn": "SSL VPN", "other": "Other"}
		canonical, exists := protocols[protocol]
		if !exists && custom != nil && custom("logical_tunnel", protocol) {
			canonical, exists = protocol, true
		}
		if !exists {
			return nil, fmt.Errorf("tunnel_protocol must be IPsec, WireGuard, GRE, VXLAN, L2TP, Geneve, IPIP, SIT, OpenVPN, ssl_vpn, other, or a custom tunnel type")
		}
		clean["tunnel_protocol"] = canonical
		if mode := strings.ToLower(text("tunnel_mode")); mode != "" {
			if mode != "tunnel" && mode != "transport" {
				return nil, fmt.Errorf("tunnel_mode must be tunnel or transport")
			}
			clean["tunnel_mode"] = mode
		}
		for _, key := range []string{"local_endpoint", "remote_endpoint"} {
			if v := text(key); v != "" {
				clean[key] = v
			}
		}
	case "provider_service":
		service := strings.ToLower(text("service_type"))
		services := map[string]string{"pls": "pls", "leased_circuit": "leased_circuit", "dark_fiber": "dark_fiber", "wavelength": "wavelength", "carrier_ethernet": "carrier_ethernet", "mpls": "mpls", "internet": "internet", "evpn": "evpn", "sd_wan": "sd_wan", "other": "other"}
		_, exists := services[service]
		if !exists && custom != nil && custom("provider_service", service) {
			exists = true
		}
		if !exists {
			return nil, fmt.Errorf("service_type must be pls, leased_circuit, dark_fiber, wavelength, carrier_ethernet, mpls, internet, evpn, sd_wan, other, or a custom provider type")
		}
		clean["service_type"] = service
		for _, key := range []string{"provider", "circuit_id", "bandwidth"} {
			if v := text(key); v != "" {
				clean[key] = v
			}
		}
	default:
		return nil, fmt.Errorf("connection_class must be physical, virtual, logical_tunnel, or provider_service")
	}
	if clean["physical_type"] == "other" || clean["virtual_type"] == "other" || clean["tunnel_protocol"] == "Other" || clean["service_type"] == "other" {
		if text("custom_type") == "" {
			return nil, fmt.Errorf("custom_type is required for an other connection type")
		}
		clean["custom_type"] = text("custom_type")
	}
	for _, key := range []string{"label", "description"} {
		if v := text(key); v != "" {
			clean[key] = v
		}
	}
	return clean, nil
}

// A normal interface is bound to one peer item across every connection class.
// Multiple logical services to that peer are allowed; a physical port still has
// one cable. Shapes provide a shared point that deliberately permits many peers.
func checkLinkAvailability(tx *sql.Tx, source, target string, spec map[string]any, exclude string) error {
	if err := validateServiceLink(tx, source, target, spec); err != nil {
		return err
	}
	var sourceDevice, targetDevice string
	if err := tx.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, source).Scan(&sourceDevice); err != nil {
		return fmt.Errorf("source interface is unavailable")
	}
	if err := tx.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, target).Scan(&targetDevice); err != nil {
		return fmt.Errorf("target interface is unavailable")
	}
	for _, end := range []struct{ port, peer, device string }{{source, targetDevice, sourceDevice}, {target, sourceDevice, targetDevice}} {
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, end.device).Scan(&kind); err != nil {
			return fmt.Errorf("interface owner is unavailable")
		}
		if kind == "shape" {
			continue
		}
		var conflicts int
		if err := tx.QueryRow(`SELECT count(*) FROM links l JOIN interfaces peer ON peer.id=CASE WHEN l.source_interface=? THEN l.target_interface ELSE l.source_interface END WHERE (l.source_interface=? OR l.target_interface=?) AND peer.device_id<>? AND (?='' OR l.id<>?)`, end.port, end.port, end.port, end.peer, exclude, exclude).Scan(&conflicts); err != nil {
			return fmt.Errorf("connection lookup failed")
		}
		if conflicts > 0 {
			return fmt.Errorf("each device interface can connect to only one peer item; choose another interface")
		}
		if spec["connection_class"] == "physical" {
			if err := tx.QueryRow(`SELECT count(*) FROM links WHERE (source_interface=? OR target_interface=?) AND `+physicalLinkPredicate+` AND (?='' OR id<>?)`, end.port, end.port, exclude, exclude).Scan(&conflicts); err != nil {
				return fmt.Errorf("connection lookup failed")
			}
			if conflicts > 0 {
				return fmt.Errorf("each device interface can belong to only one physical connection")
			}
		}
	}
	return nil
}

const physicalLinkPredicate = `COALESCE(json_extract(data,'$.connection_class'),'physical')='physical'`

func (a *App) search(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if memberKind := r.URL.Query().Get("members_kind"); memberKind != "" {
		field := map[string]string{"site": "site_id", "building": "building_id", "telecom_room": "telecom_room_id"}[memberKind]
		if field == "" {
			http.Error(w, "unsupported location kind", 400)
			return
		}
		group := r.URL.Query().Get("grouping")
		if group == "" {
			group = "location"
		}
		value, name := r.URL.Query().Get("members_id"), r.URL.Query().Get("members_name")
		rows, err := db.Query(`SELECT o.id,o.kind,o.name,COALESCE(l.x,o.x),COALESCE(l.y,o.y),o.data,o.version FROM objects o LEFT JOIN layouts l ON l.object_id=o.id AND l.grouping=? WHERE o.kind IN ('device','virtual_machine','virtual_switch') AND (json_extract(o.data,'$.`+field+`')=? OR json_extract(o.data,'$.`+field+`')=?) ORDER BY o.name LIMIT 20`, group, value, name)
		if err != nil {
			http.Error(w, "location member lookup failed", 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, kind, title, raw string
			var x, y float64
			var version int
			if rows.Scan(&id, &kind, &title, &x, &y, &raw, &version) == nil {
				var data any
				_ = json.Unmarshal([]byte(raw), &data)
				out = append(out, map[string]any{"id": id, "kind": kind, "name": title, "x": x, "y": y, "data": data, "version": version})
			}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	q := "%" + r.URL.Query().Get("q") + "%"
	group := r.URL.Query().Get("grouping")
	if group == "" {
		group = "location"
	}
	where := `o.name LIKE ?`
	if r.URL.Query().Get("scope") == "devices" {
		where += ` AND o.kind IN ('device','virtual_machine','virtual_switch','patch_panel')`
	}
	rows, e := db.Query(`SELECT o.id,o.kind,o.name,COALESCE(l.x,o.x),COALESCE(l.y,o.y),o.data,o.version FROM objects o LEFT JOIN layouts l ON l.object_id=o.id AND l.grouping=? WHERE `+where+` ORDER BY o.name LIMIT 100`, group, q)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, k, n string
		var x, y float64
		var data string
		var version int
		rows.Scan(&id, &k, &n, &x, &y, &data, &version)
		var meta any
		_ = json.Unmarshal([]byte(data), &meta)
		out = append(out, map[string]any{"id": id, "kind": k, "name": n, "x": x, "y": y, "data": meta, "version": version})
	}
	json.NewEncoder(w).Encode(out)
}
func (a *App) audit(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p, ok := a.identity(r); !ok || p.Role != "admin" {
		http.Error(w, "administrator required", http.StatusForbidden)
		return
	}
	rows, e := db.Query(`SELECT id,actor,action,object_id,data,created FROM audit ORDER BY id DESC LIMIT 500`)
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, ts int64
		var actor, act, obj, data string
		rows.Scan(&id, &actor, &act, &obj, &data, &ts)
		out = append(out, map[string]any{"id": id, "actor": actor, "action": act, "object": obj, "data": data, "created": ts})
	}
	json.NewEncoder(w).Encode(out)
}

func (a *App) undo(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method != http.MethodPost || p.Role == "viewer" {
		http.Error(w, "editor POST required", http.StatusForbidden)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var auditID int64
	var objectID, raw string
	if err = tx.QueryRow(`SELECT id,object_id,data FROM audit WHERE actor=? AND action='object_upsert' ORDER BY id DESC LIMIT 1`, p.User).Scan(&auditID, &objectID, &raw); err != nil {
		http.Error(w, "no undoable edit", 404)
		return
	}
	var latest int64
	if err = tx.QueryRow(`SELECT id FROM audit WHERE object_id=? ORDER BY id DESC LIMIT 1`, objectID).Scan(&latest); err != nil || latest != auditID {
		http.Error(w, "object changed since that edit; undo is unsafe", 409)
		return
	}
	var snapshot struct {
		Before            json.RawMessage         `json:"before"`
		DependentStamp    string                  `json:"dependent_stamp"`
		BudgetChanges     []interfaceBudgetChange `json:"interface_budget_changes"`
		CreatedInterfaces []struct {
			ID string `json:"id"`
		} `json:"created_interfaces"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		http.Error(w, "edit has no recoverable snapshot", 409)
		return
	}
	var currentKind string
	if e := tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, objectID).Scan(&currentKind); e != nil {
		http.Error(w, "object no longer exists", 409)
		return
	}
	if snapshot.DependentStamp != "" {
		stamp, e := topologyDependentStamp(tx, objectID, currentKind)
		if e != nil || stamp != snapshot.DependentStamp {
			http.Error(w, "contained inventory changed; undo is unsafe", 409)
			return
		}
	}
	if len(snapshot.Before) == 0 || string(snapshot.Before) == "null" {
		if e := topologyDeleteGuard(tx, objectID, currentKind); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if n, e := locationChildCount(tx, objectID); e != nil || n > 0 {
			http.Error(w, "object contains inventory; undo is unsafe", 409)
			return
		}
		trackedInterfaces := make(map[string]struct{}, len(snapshot.CreatedInterfaces))
		for _, iface := range snapshot.CreatedInterfaces {
			if iface.ID != "" {
				trackedInterfaces[iface.ID] = struct{}{}
			}
		}
		rows, queryErr := tx.Query(`SELECT id FROM interfaces WHERE device_id=?`, objectID)
		if queryErr != nil {
			http.Error(w, "interface reference check failed", 500)
			return
		}
		for rows.Next() {
			var interfaceID string
			if rows.Scan(&interfaceID) != nil {
				rows.Close()
				http.Error(w, "interface reference check failed", 500)
				return
			}
			if _, ok := trackedInterfaces[interfaceID]; !ok {
				rows.Close()
				http.Error(w, "object has dependent interfaces or routes; undo is unsafe", 409)
				return
			}
		}
		if rows.Err() != nil {
			rows.Close()
			http.Error(w, "interface reference check failed", 500)
			return
		}
		rows.Close()
		var refs int
		if err = tx.QueryRow(`SELECT (SELECT count(*) FROM interfaces i WHERE i.device_id=? AND (EXISTS(SELECT 1 FROM links l WHERE l.source_interface=i.id OR l.target_interface=i.id) OR EXISTS(SELECT 1 FROM patch_pairings p WHERE p.port_a=i.id OR p.port_b=i.id))) + (SELECT count(*) FROM cable_routes WHERE infrastructure_id=?) + (SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?)`, objectID, objectID, objectID).Scan(&refs); err != nil {
			http.Error(w, "reference check failed", 500)
			return
		}
		if refs != 0 {
			http.Error(w, "object has dependent interfaces or routes; undo is unsafe", 409)
			return
		}
		if _, err = tx.Exec(`DELETE FROM interfaces WHERE device_id=?`, objectID); err == nil {
			_, err = tx.Exec(`DELETE FROM layouts WHERE object_id=?`, objectID)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM objects WHERE id=?`, objectID)
		}
	} else {
		var v map[string]any
		if json.Unmarshal(snapshot.Before, &v) != nil {
			http.Error(w, "snapshot invalid", 409)
			return
		}
		for _, change := range snapshot.BudgetChanges {
			var current string
			if e := tx.QueryRow(`SELECT data FROM interfaces WHERE id=? AND device_id=?`, change.ID, objectID).Scan(&current); e != nil || current != change.After {
				http.Error(w, "interface allocations changed; undo is unsafe", 409)
				return
			}
			if _, e := tx.Exec(`UPDATE interfaces SET data=? WHERE id=?`, change.Before, change.ID); e != nil {
				http.Error(w, "interface allocation restore failed", 500)
				return
			}
		}
		if e := normalizeTopologyPlacement(tx, objectID, fmt.Sprint(v["kind"]), v); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		if e := normalizeDeviceVRFs(tx, objectID, fmt.Sprint(v["kind"]), v); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		b, _ := json.Marshal(v)
		_, err = tx.Exec(`UPDATE objects SET kind=?,name=?,data=?,x=?,y=?,version=version+1 WHERE id=?`, v["kind"], v["name"], string(b), v["x"], v["y"], objectID)
	}
	if err != nil {
		http.Error(w, "undo failed", 500)
		return
	}
	if len(snapshot.Before) > 0 && string(snapshot.Before) != "null" {
		var before map[string]any
		_ = json.Unmarshal(snapshot.Before, &before)
		if e := refreshTopologyDescendants(tx, objectID, fmt.Sprint(before["kind"])); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
	}
	if err = a.recordAudit(tx, p.User, "undo", objectID, map[string]any{"undo_of": auditID, "snapshot": snapshot.Before}); err != nil {
		http.Error(w, "undo audit failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]string{"type": "undo", "id": objectID})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "undo commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) users(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role != "admin" {
		http.Error(w, "administrator required", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		rows, err := db.Query(`SELECT id,username,role FROM users ORDER BY username`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, username, role string
			if rows.Scan(&id, &username, &role) == nil {
				out = append(out, map[string]string{"id": id, "username": username, "role": role})
			}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if r.Method == http.MethodDelete {
		username := r.URL.Query().Get("username")
		if username == "" || username == p.User {
			http.Error(w, "username is required and administrators cannot disable their own account", 400)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		var role string
		if err = tx.QueryRow(`SELECT role FROM users WHERE username=?`, username).Scan(&role); err != nil {
			http.Error(w, "user not found", 404)
			return
		}
		if role == "admin" {
			var admins int
			if err = tx.QueryRow(`SELECT count(*) FROM users WHERE role='admin'`).Scan(&admins); err != nil || admins <= 1 {
				http.Error(w, "cannot disable the last administrator", 409)
				return
			}
		}
		if _, err = tx.Exec(`DELETE FROM users WHERE username=?`, username); err != nil {
			http.Error(w, "user disable failed", 500)
			return
		}
		if err = a.recordAudit(tx, p.User, "user_disable", username, map[string]any{"before": map[string]string{"username": username, "role": role}, "after": nil}); err != nil {
			http.Error(w, "audit write failed", 500)
			return
		}
		if err = a.commitLogged(tx); err != nil {
			http.Error(w, "user disable commit failed", 500)
			return
		}
		a.revokeUserSessions(username)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct{ Username, Password, Role string }
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Username == "" || (input.Role != "admin" && input.Role != "editor" && input.Role != "viewer") || (r.Method == http.MethodPost && len(input.Password) < 12) || (r.Method == http.MethodPut && input.Password != "" && len(input.Password) < 12) {
		http.Error(w, "username, 12+ character password when setting one, and valid role are required", 400)
		return
	}
	var passwordHash string
	if input.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "password processing failed", 500)
			return
		}
		passwordHash = string(hash)
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	beforeRole := ""
	if r.Method == http.MethodPost {
		if _, err = tx.Exec(`INSERT INTO users(id,username,password,role) VALUES(?,?,?,?)`, newID(), input.Username, passwordHash, input.Role); err != nil {
			http.Error(w, "user could not be created", 409)
			return
		}
	} else {
		if err = tx.QueryRow(`SELECT role FROM users WHERE username=?`, input.Username).Scan(&beforeRole); err != nil {
			http.Error(w, "user not found", 404)
			return
		}
		if beforeRole == "admin" && input.Role != "admin" {
			var admins int
			if err = tx.QueryRow(`SELECT count(*) FROM users WHERE role='admin'`).Scan(&admins); err != nil || admins <= 1 {
				http.Error(w, "cannot demote the last administrator", 409)
				return
			}
		}
		if input.Password == "" {
			_, err = tx.Exec(`UPDATE users SET role=? WHERE username=?`, input.Role, input.Username)
		} else {
			_, err = tx.Exec(`UPDATE users SET password=?,role=? WHERE username=?`, passwordHash, input.Role, input.Username)
		}
		if err != nil {
			http.Error(w, "user could not be saved", 500)
			return
		}
	}
	var before any
	if beforeRole != "" {
		before = map[string]string{"username": input.Username, "role": beforeRole}
	}
	if err = a.recordAudit(tx, p.User, "user_"+strings.ToLower(r.Method), input.Username, map[string]any{"before": before, "after": map[string]string{"username": input.Username, "role": input.Role}, "password_changed": input.Password != ""}); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "user save commit failed", 500)
		return
	}
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	} else {
		a.revokeUserSessions(input.Username)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) revokeUserSessions(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, session := range a.sessions {
		if session.User == username {
			delete(a.sessions, id)
			delete(a.sessionExpires, id)
		}
	}
}

func (a *App) csvImport(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" {
		http.Error(w, "editor role required", 403)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST CSV body with kind,name[,x,y] header", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	cr := csv.NewReader(r.Body)
	cr.FieldsPerRecord = -1
	head, e := cr.Read()
	if e != nil {
		http.Error(w, "CSV header required", 400)
		return
	}
	idx := map[string]int{}
	for i, k := range head {
		idx[strings.ToLower(strings.TrimSpace(k))] = i
	}
	if _, ok := idx["kind"]; !ok {
		http.Error(w, "kind column required", 400)
		return
	}
	type importRow struct {
		id, kind, name, deviceID, source, target, panelID, portA, portB, linkID, infrastructureID string
		medium, cableType, connectorA, connectorB                                                 string
		sequence                                                                                  *int
		x, y                                                                                      float64
		data                                                                                      string
	}
	items := []importRow{}
	for {
		row, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			http.Error(w, "malformed CSV: "+e.Error(), 400)
			return
		}
		if len(row) == 0 {
			break
		}
		get := func(k string) string {
			i, ok := idx[k]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		kind, name := strings.ToLower(get("kind")), get("name")
		if kind == "" {
			http.Error(w, fmt.Sprintf("row %d needs kind", len(items)+2), 400)
			return
		}
		id := get("id")
		if id == "" {
			id = newID()
		}
		var x, y float64
		for column, target := range map[string]*float64{"x": &x, "y": &y} {
			if raw := get(column); raw != "" {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					http.Error(w, fmt.Sprintf("row %d has invalid %s coordinate", len(items)+2, column), 400)
					return
				}
				*target = value
			}
		}
		metadata := map[string]any{"source": "csv"}
		if raw := get("metadata_json"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
				http.Error(w, "metadata_json must contain a JSON object", 400)
				return
			}
		}
		reserved := map[string]bool{"metadata_json": true, "kind": true, "id": true, "name": true, "x": true, "y": true, "device_id": true, "source_interface": true, "target_interface": true, "panel_id": true, "port_a": true, "port_b": true, "link_id": true, "infrastructure_id": true, "sequence": true, "medium": true, "physical_type": true, "connection_class": true, "cable_type": true, "connector_a": true, "connector_b": true, "strand_count": true, "length_m": true, "part_number": true, "shielding": true, "tunnel_protocol": true, "tunnel_mode": true, "local_endpoint": true, "remote_endpoint": true, "service_type": true, "provider": true, "circuit_id": true, "bandwidth": true, "virtual_type": true, "vlan_id": true, "vni": true, "network_name": true, "custom_type": true, "label": true}
		for column, index := range idx {
			if !reserved[column] && index < len(row) && strings.TrimSpace(row[index]) != "" {
				metadata[column] = strings.TrimSpace(row[index])
			}
		}
		data, _ := json.Marshal(metadata)
		v := importRow{id: id, kind: kind, name: name, deviceID: get("device_id"), source: get("source_interface"), target: get("target_interface"), panelID: get("panel_id"), portA: get("port_a"), portB: get("port_b"), linkID: get("link_id"), infrastructureID: get("infrastructure_id"), medium: get("medium"), cableType: get("cable_type"), connectorA: get("connector_a"), connectorB: get("connector_b"), x: x, y: y, data: string(data)}
		if get("sequence") != "" {
			var sequence int
			if _, err := fmt.Sscan(get("sequence"), &sequence); err != nil || sequence < 0 {
				http.Error(w, fmt.Sprintf("row %d has invalid sequence", len(items)+2), 400)
				return
			}
			v.sequence = &sequence
		}
		switch kind {
		case "relationship":
			if metadata["source_id"] == nil || metadata["target_id"] == nil {
				http.Error(w, "relationship endpoints required", 400)
				return
			}
		case "geography":
			if metadata["owner_type"] == nil || metadata["data"] == nil {
				http.Error(w, "geography row requires a geographic record in metadata_json", 400)
				return
			}
		case "interface":
			if v.deviceID == "" || name == "" {
				http.Error(w, "interface rows require device_id and name", 400)
				return
			}
		case "connection", "link":
			if v.source == "" || v.target == "" || v.source == v.target {
				http.Error(w, "connection rows require two distinct source_interface and target_interface values", 400)
				return
			}
			rawSpec := metadata
			for _, key := range []string{"connection_class", "medium", "physical_type", "cable_type", "connector_a", "connector_b", "strand_count", "length_m", "part_number", "shielding", "tunnel_protocol", "tunnel_mode", "local_endpoint", "remote_endpoint", "service_type", "provider", "circuit_id", "bandwidth", "virtual_type", "vlan_id", "vni", "network_name", "custom_type", "label", "description"} {
				if value := get(key); value != "" {
					rawSpec[key] = value
				}
			}
			spec, err := validateLinkSpecWithDB(rawSpec, db)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			encoded, _ := json.Marshal(spec)
			v.data = string(encoded)
		case "pairing":
			if v.panelID == "" || v.portA == "" || v.portB == "" || v.portA == v.portB {
				http.Error(w, "pairing rows require panel_id, port_a, and port_b", 400)
				return
			}
		case "route":
			if v.linkID == "" || v.infrastructureID == "" {
				http.Error(w, "route rows require link_id and infrastructure_id", 400)
				return
			}
		default:
			if name == "" {
				http.Error(w, fmt.Sprintf("row %d inventory object requires name", len(items)+2), 400)
				return
			}
		}
		items = append(items, v)
	}
	dryRun := r.URL.Query().Get("dry_run") == "1"
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	sequences := map[string]int{}
	for _, v := range items {
		switch v.kind {
		case "relationship":
			var relation topologyRelationship
			if json.Unmarshal([]byte(v.data), &relation) != nil {
				http.Error(w, "invalid relationship", 400)
				return
			}
			relation.ID = v.id
			if err := validateRelationship(tx, relation); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if _, e = tx.Exec(`INSERT INTO relationships(id,source_id,target_id,relationship_type,version,label) VALUES(?,?,?,?,1,?)`, relation.ID, relation.Source, relation.Target, relation.Type, relation.Label); e != nil {
				http.Error(w, "relationship import failed", 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "relationship_import", relation.ID, relation); e != nil {
				http.Error(w, "relationship audit failed", 500)
				return
			}
		case "geography":
			var g archivedGeographyRecord
			if json.Unmarshal([]byte(v.data), &g) != nil || g.ID != v.id {
				http.Error(w, "invalid geographic import record", 400)
				return
			}
			if err := importArchivedGeography(tx, &g); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "geography_import", "geo:"+g.Type+":"+g.ID, g); e != nil {
				http.Error(w, "geographic import audit failed", 500)
				return
			}
		case "interface":
			var deviceKind string
			if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, v.deviceID).Scan(&deviceKind) != nil || !interfaceOwnerKindAllowed(deviceKind) {
				http.Error(w, "interface device_id must reference a device, VM, patch panel, shape, text label, or manhole", 400)
				return
			}
			if deviceKind == "shape" {
				if err := checkShapeInterface(tx, v.deviceID, v.id); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
			}
			var ownerRaw string
			if e = tx.QueryRow(`SELECT data FROM objects WHERE id=?`, v.deviceID).Scan(&ownerRaw); e != nil {
				http.Error(w, "interface owner not found", 400)
				return
			}
			var owner, port map[string]any
			_ = json.Unmarshal([]byte(ownerRaw), &owner)
			decodedPort, decodeErr := decodeInterfaceMetadata(v.data)
			if decodeErr != nil {
				http.Error(w, decodeErr.Error(), 400)
				return
			}
			port = decodedPort
			if e = normalizeInterfaceVRFs(owner, port); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			if _, e = normalizeInterfaceCapacity(deviceKind, owner, port, false); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			encodedPort, _ := json.Marshal(port)
			v.data = string(encodedPort)
			if _, e = tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?)`, v.id, v.deviceID, v.name, v.data); e != nil {
				http.Error(w, "interface import failed; import its device first", 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "import_interface_create", v.id, map[string]any{"before": nil, "after": map[string]any{"id": v.id, "device_id": v.deviceID, "name": v.name, "data": json.RawMessage(v.data)}}); e != nil {
				http.Error(w, "import audit failed", 500)
				return
			}
		case "connection", "link":
			var sourceDevice, targetDevice string
			if tx.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, v.source).Scan(&sourceDevice) != nil || tx.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, v.target).Scan(&targetDevice) != nil || (sourceDevice == targetDevice && jsonClass(v.data) == "physical") {
				http.Error(w, "connection endpoints must be existing interfaces on different devices", 400)
				return
			}
			var spec map[string]any
			_ = json.Unmarshal([]byte(v.data), &spec)
			if err := checkLinkAvailability(tx, v.source, v.target, spec, ""); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if _, e = tx.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES(?,?,?,?)`, v.id, v.source, v.target, v.data); e != nil {
				http.Error(w, "connection import failed; each interface can have one cable endpoint", 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "import_link_create", v.id, map[string]any{"before": nil, "after": map[string]any{"id": v.id, "source_interface": v.source, "target_interface": v.target, "data": json.RawMessage(v.data)}}); e != nil {
				http.Error(w, "import audit failed", 500)
				return
			}
		case "pairing":
			var objectKind string
			if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, v.panelID).Scan(&objectKind) != nil || objectKind != "patch_panel" {
				http.Error(w, "pairing panel must reference an existing patch_panel", 400)
				return
			}
			for _, port := range []string{v.portA, v.portB} {
				var owner string
				if tx.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, port).Scan(&owner) != nil || owner != v.panelID {
					http.Error(w, "pairing ports must be interfaces on the specified patch panel", 400)
					return
				}
			}
			var paired int
			if tx.QueryRow(`SELECT count(*) FROM patch_pairings WHERE port_a IN (?,?) OR port_b IN (?,?)`, v.portA, v.portB, v.portA, v.portB).Scan(&paired) != nil || paired != 0 {
				http.Error(w, "a panel port can be paired only once", 400)
				return
			}
			if _, e = tx.Exec(`INSERT INTO patch_pairings VALUES(?,?,?,?)`, v.id, v.panelID, v.portA, v.portB); e != nil {
				http.Error(w, "patch-panel pairing import failed", 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "import_pairing_create", v.id, map[string]any{"before": nil, "after": map[string]string{"id": v.id, "panel_id": v.panelID, "port_a": v.portA, "port_b": v.portB}}); e != nil {
				http.Error(w, "import audit failed", 500)
				return
			}
		case "route":
			var linkExists int
			if tx.QueryRow(`SELECT count(*) FROM links WHERE id=? AND `+physicalLinkPredicate, v.linkID).Scan(&linkExists) != nil || linkExists == 0 {
				http.Error(w, "route link_id must reference a physical connection", 400)
				return
			}
			var objectKind string
			if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, v.infrastructureID).Scan(&objectKind) != nil || (objectKind != "conduit" && objectKind != "manhole") {
				http.Error(w, "route infrastructure must reference a conduit or manhole", 400)
				return
			}
			sequence := sequences[v.linkID]
			if v.sequence != nil {
				sequence = *v.sequence
			}
			sequences[v.linkID] = sequence + 1
			if _, e = tx.Exec(`INSERT INTO cable_routes VALUES(?,?,?,?)`, v.id, v.linkID, sequence, v.infrastructureID); e != nil {
				http.Error(w, "route import failed; connection must already exist", 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "import_route_create", v.id, map[string]any{"before": nil, "after": map[string]any{"id": v.id, "link_id": v.linkID, "sequence": sequence, "infrastructure_id": v.infrastructureID}}); e != nil {
				http.Error(w, "import audit failed", 500)
				return
			}
		default:
			var objectMeta map[string]any
			_ = json.Unmarshal([]byte(v.data), &objectMeta)
			if err := normalizeTopologyPlacement(tx, v.id, v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := normalizeLocationParent(tx, v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := normalizeRackPlacement(tx, v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := normalizeConduitEndpoints(tx, v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := requirePlacement(v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if true {
				encoded, _ := json.Marshal(objectMeta)
				v.data = string(encoded)
			}
			if v.kind == "shape" {
				if err := validateShapeSpec(objectMeta); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				encoded, _ := json.Marshal(objectMeta)
				v.data = string(encoded)
			}
			if v.kind == "label" {
				if len([]rune(v.name)) > 2000 {
					http.Error(w, "label text must be at most 2000 characters", 400)
					return
				}
				if err := validateLabelSpec(objectMeta); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				encoded, _ := json.Marshal(objectMeta)
				v.data = string(encoded)
			}
			if typeID, _ := objectMeta["device_type_id"].(string); typeID != "" {
				var typeKind, typeName, visual, medium string
				if tx.QueryRow(`SELECT object_kind,name,visual,medium FROM device_types WHERE id=? AND enabled=1`, typeID).Scan(&typeKind, &typeName, &visual, &medium) != nil || typeKind != v.kind {
					http.Error(w, "imported network item type does not match its kind", 400)
					return
				}
				objectMeta["device_type"] = typeName
				objectMeta["device_visual"] = visual
				objectMeta["medium"] = medium
				encoded, _ := json.Marshal(objectMeta)
				v.data = string(encoded)
			}
			if err := normalizeDeviceVRFs(tx, v.id, v.kind, objectMeta); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			encodedVRFs, _ := json.Marshal(objectMeta)
			v.data = string(encodedVRFs)
			if hostedKind(v.kind) {
				var meta map[string]any
				_ = json.Unmarshal([]byte(v.data), &meta)
				if err := prepareHostedObject(tx, v.id, v.kind, meta); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				encoded, _ := json.Marshal(meta)
				v.data = string(encoded)
			}
			if v.kind == "device" {
				var virtualMachines int
				if e = tx.QueryRow(`SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?`, v.id).Scan(&virtualMachines); e != nil {
					http.Error(w, "hypervisor child reference check failed", 500)
					return
				}
				var hostMeta map[string]any
				_ = json.Unmarshal([]byte(v.data), &hostMeta)
				if virtualMachines > 0 && !isHypervisorMeta(hostMeta) {
					http.Error(w, "cannot change a hypervisor with VMs or vSwitches to another device type", 409)
					return
				}
			}
			var oldKind, oldName, oldData string
			var oldX, oldY float64
			var before any
			lookupErr := tx.QueryRow(`SELECT kind,name,data,x,y FROM objects WHERE id=?`, v.id).Scan(&oldKind, &oldName, &oldData, &oldX, &oldY)
			if lookupErr == nil && oldKind != v.kind {
				var refs int
				if n, err := locationChildCount(tx, v.id); err != nil || n > 0 {
					http.Error(w, "cannot change a container type while it contains placed items", 409)
					return
				}
				if err := tx.QueryRow(`SELECT (SELECT count(*) FROM interfaces WHERE device_id=?) + (SELECT count(*) FROM cable_routes WHERE infrastructure_id=?) + (SELECT count(*) FROM objects WHERE kind IN ('virtual_machine','virtual_switch') AND json_extract(data,'$.host_id')=?)`, v.id, v.id, v.id).Scan(&refs); err != nil || refs > 0 {
					http.Error(w, "cannot change the type of an object with interfaces or route references", 409)
					return
				}
			}
			if lookupErr == nil {
				before = map[string]any{"kind": oldKind, "name": oldName, "data": json.RawMessage(oldData), "x": oldX, "y": oldY}
			} else if lookupErr != sql.ErrNoRows {
				http.Error(w, "inventory lookup failed", 500)
				return
			}
			if _, e = tx.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,name=excluded.name,data=excluded.data,x=excluded.x,y=excluded.y,version=objects.version+1`, v.id, v.kind, v.name, v.data, v.x, v.y); e != nil {
				http.Error(w, "inventory import failed", 400)
				return
			}

			if v.kind == "building" && lookupErr == sql.ErrNoRows {
				if err := ensureGround(tx, v.id); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
			if err := refreshTopologyDescendants(tx, v.id, v.kind); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if e = a.recordAudit(tx, p.User, "import_object_upsert", v.id, map[string]any{"before": before, "after": map[string]any{"kind": v.kind, "name": v.name, "data": json.RawMessage(v.data), "x": v.x, "y": v.y}}); e != nil {
				http.Error(w, "import audit failed", 500)
				return
			}
		}
	}
	for _, item := range items {
		if item.kind == "device" || item.kind == "virtual_switch" {
			var owner map[string]any
			_ = json.Unmarshal([]byte(item.data), &owner)
			changes, err := reconcileInterfaceBudgets(tx, item.id, owner)
			if err != nil {
				http.Error(w, "interface allocation update failed", 500)
				return
			}
			if len(changes) > 0 {
				if err = a.recordAudit(tx, p.User, "import_interface_budgets", item.id, changes); err != nil {
					http.Error(w, "audit failed", 500)
					return
				}
			}
		}
	}
	for _, item := range items {
		if item.kind == "outlet" || item.kind == "service" {
			var m map[string]any
			_ = json.Unmarshal([]byte(item.data), &m)
			if err := ensureTopologyPorts(tx, item.id, item.kind, m); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		if item.kind == "shape" {
			if err := ensureShapeInterface(tx, item.id); err != nil {
				http.Error(w, "shape connection point creation failed", 500)
				return
			}
		}
	}
	if dryRun {
		_ = a.rollbackLogged(tx)
		json.NewEncoder(w).Encode(map[string]any{"valid": true, "rows": len(items), "supported_kinds": []string{"site", "building", "floor", "room", "outlet", "service", "generator", "ups", "relationship", "telecom_room", "rack", "device", "virtual_machine", "virtual_switch", "patch_panel", "conduit", "manhole", "label", "shape", "interface", "connection", "pairing", "route"}})
		return
	}
	if e = a.recordAudit(tx, p.User, "import", "", map[string]any{"rows": len(items)}); e != nil {
		http.Error(w, "import audit failed", 500)
		return
	}
	event := []byte(`{"type":"import","count":` + strconv.Itoa(len(items)) + `}`)
	seq, e := appendEvent(tx, event)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "import transaction failed", 500)
		return
	}
	a.notify(seq, event)
	json.NewEncoder(w).Encode(map[string]int{"imported": len(items)})
}

// Pairings record the internal connection between two ports on a patch panel.
func (a *App) pairings(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == "GET" {
		panel := r.URL.Query().Get("panel_id")
		rows, e := db.Query(`SELECT id,panel_id,port_a,port_b FROM patch_pairings WHERE panel_id=?`, panel)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, p, a, b string
			rows.Scan(&id, &p, &a, &b)
			out = append(out, map[string]string{"id": id, "panel_id": p, "port_a": a, "port_b": b})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", 403)
		return
	}
	if r.Method == http.MethodDelete {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "pairing id required", 400)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		var panel, portA, portB string
		if err = tx.QueryRow(`SELECT panel_id,port_a,port_b FROM patch_pairings WHERE id=?`, id).Scan(&panel, &portA, &portB); err != nil {
			http.Error(w, "pairing not found", 404)
			return
		}
		if _, err = tx.Exec(`DELETE FROM patch_pairings WHERE id=?`, id); err != nil {
			http.Error(w, "pairing delete failed", 500)
			return
		}
		if err = a.recordAudit(tx, p.User, "pairing_delete", panel, map[string]string{"panel_id": panel, "port_a": portA, "port_b": portB}); err != nil {
			http.Error(w, "audit write failed", 500)
			return
		}
		event, _ := json.Marshal(map[string]string{"type": "pairing_delete", "panel_id": panel})
		seq, err := appendEvent(tx, event)
		if err != nil {
			http.Error(w, "event write failed", 500)
			return
		}
		if err = a.commitLogged(tx); err != nil {
			http.Error(w, "commit failed", 500)
			return
		}
		a.notify(seq, event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var x struct {
		Panel string `json:"panel_id"`
		A     string `json:"port_a"`
		B     string `json:"port_b"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.Panel == "" || x.A == "" || x.B == "" || x.A == x.B {
		http.Error(w, "panel and two distinct ports required", 400)
		return
	}
	var kind string
	if db.QueryRow(`SELECT kind FROM objects WHERE id=?`, x.Panel).Scan(&kind) != nil || kind != "patch_panel" {
		http.Error(w, "panel_id must reference a patch_panel", 400)
		return
	}
	for _, port := range []string{x.A, x.B} {
		var owner string
		if db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, port).Scan(&owner) != nil || owner != x.Panel {
			http.Error(w, "both ports must be interfaces belonging to this patch panel", 400)
			return
		}
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var paired int
	if e = tx.QueryRow(`SELECT count(*) FROM patch_pairings WHERE port_a IN (?,?) OR port_b IN (?,?)`, x.A, x.B, x.A, x.B).Scan(&paired); e != nil {
		http.Error(w, "pairing lookup failed", 500)
		return
	}
	if paired != 0 {
		http.Error(w, "a panel port can be paired only once", 409)
		return
	}
	_, e = tx.Exec(`INSERT INTO patch_pairings VALUES(?,?,?,?)`, newID(), x.Panel, x.A, x.B)
	if e != nil {
		http.Error(w, "port already paired", 409)
		return
	}
	if e = a.recordAudit(tx, p.User, "pairing_create", x.Panel, map[string]any{"panel_id": x.Panel, "port_a": x.A, "port_b": x.B}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]any{"type": "pairing", "panel_id": x.Panel})
	seq, e := appendEvent(tx, event)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(201)
}

// Routes store ordered conduit/manhole objects for a physical cable link.
func (a *App) routes(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == "GET" {
		link := r.URL.Query().Get("link_id")
		limit := 2000
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			limit = n
		}
		if limit > 5000 {
			limit = 5000
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		var rows *sql.Rows
		var e error
		if link != "" {
			rows, e = db.Query(`SELECT r.id,r.link_id,r.sequence,r.infrastructure_id,COALESCE(o.name,''),COALESCE(o.kind,'') FROM cable_routes r LEFT JOIN objects o ON o.id=r.infrastructure_id WHERE r.link_id=? ORDER BY r.sequence LIMIT ? OFFSET ?`, link, limit, offset)
		} else if list := r.URL.Query().Get("link_ids"); list != "" {
			ids := strings.Split(list, ",")
			if len(ids) > 500 {
				http.Error(w, "request at most 500 link IDs", 400)
				return
			}
			placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
			args := make([]any, len(ids))
			for i, id := range ids {
				args[i] = id
			}
			args = append(args, limit, offset)
			rows, e = db.Query(`SELECT r.id,r.link_id,r.sequence,r.infrastructure_id,COALESCE(o.name,''),COALESCE(o.kind,'') FROM cable_routes r LEFT JOIN objects o ON o.id=r.infrastructure_id WHERE r.link_id IN (`+placeholders+`) ORDER BY r.link_id,r.sequence LIMIT ? OFFSET ?`, args...)
		} else {
			rows, e = db.Query(`SELECT r.id,r.link_id,r.sequence,r.infrastructure_id,COALESCE(o.name,''),COALESCE(o.kind,'') FROM cable_routes r LEFT JOIN objects o ON o.id=r.infrastructure_id ORDER BY r.link_id,r.sequence LIMIT ? OFFSET ?`, limit, offset)
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, routeLink, infra, name, kind string
			var seq int
			rows.Scan(&id, &routeLink, &seq, &infra, &name, &kind)
			out = append(out, map[string]any{"id": id, "link_id": routeLink, "sequence": seq, "infrastructure_id": infra, "name": name, "kind": kind})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		Link     string    `json:"link_id"`
		Items    []string  `json:"infrastructure_ids"`
		Expected *[]string `json:"expected_infrastructure_ids"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil || x.Link == "" {
		http.Error(w, "link_id and infrastructure_ids required", 400)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	var linkCount int
	if e = tx.QueryRow(`SELECT count(*) FROM links WHERE id=? AND `+physicalLinkPredicate, x.Link).Scan(&linkCount); e != nil || linkCount != 1 {
		http.Error(w, "link_id must reference an existing physical connection", 400)
		return
	}
	var before []string
	// Route replacement is one atomic edit; retain the previous ordered route in the audit record.
	oldRows, e := tx.Query(`SELECT infrastructure_id FROM cable_routes WHERE link_id=? ORDER BY sequence`, x.Link)
	if e != nil {
		http.Error(w, "route lookup failed", 500)
		return
	}
	for oldRows.Next() {
		var id string
		if oldRows.Scan(&id) == nil {
			before = append(before, id)
		}
	}
	oldRows.Close()
	if x.Expected != nil {
		same := len(before) == len(*x.Expected)
		if same {
			for i := range before {
				if before[i] != (*x.Expected)[i] {
					same = false
					break
				}
			}
		}
		if !same {
			http.Error(w, "cable route changed; reload before saving", 409)
			return
		}
	}

	if _, e = tx.Exec(`DELETE FROM cable_routes WHERE link_id=?`, x.Link); e != nil {
		http.Error(w, "route update failed", 500)
		return
	}
	for i, id := range x.Items {
		var kind string
		if tx.QueryRow(`SELECT kind FROM objects WHERE id=?`, id).Scan(&kind) != nil || (kind != "conduit" && kind != "manhole") {
			http.Error(w, "route entries must reference conduit or manhole objects", 400)
			return
		}
		if _, e = tx.Exec(`INSERT INTO cable_routes VALUES(?,?,?,?)`, newID(), x.Link, i, id); e != nil {
			http.Error(w, "route save failed", 500)
			return
		}
	}
	if e = a.recordAudit(tx, p.User, "route_replace", x.Link, map[string]any{"before": before, "after": x.Items}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]any{"type": "route", "link_id": x.Link})
	seq, e := appendEvent(tx, event)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "route commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}

// trace follows cable segments and patch-panel port pairings until it reaches a device interface.
func (a *App) trace(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	start := r.URL.Query().Get("interface_id")
	selectedLink := r.URL.Query().Get("link_id")
	if start == "" {
		http.Error(w, "interface_id required", 400)
		return
	}
	type segment struct {
		LinkID string              `json:"link_id"`
		From   string              `json:"from_interface"`
		To     string              `json:"to_interface"`
		Route  []map[string]string `json:"route"`
	}
	segments := []segment{}
	visited := map[string]bool{}
	current := start
	destination := ""
	for hop := 0; hop < 64; hop++ {
		if visited[current] {
			http.Error(w, "cable trace cycle detected", 409)
			return
		}
		visited[current] = true
		var id, source, target, data string
		query := `SELECT id,source_interface,target_interface,data FROM links WHERE (source_interface=? OR target_interface=?) AND ` + physicalLinkPredicate
		args := []any{current, current}
		if hop == 0 && selectedLink != "" {
			query = `SELECT id,source_interface,target_interface,data FROM links WHERE (source_interface=? OR target_interface=?) AND id=?`
			args = append(args, selectedLink)
		}
		if hop == 0 && selectedLink == "" {
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM links WHERE (source_interface=? OR target_interface=?) AND `+physicalLinkPredicate, current, current).Scan(&count); err != nil {
				http.Error(w, "connection lookup failed", 500)
				return
			}
			if count > 1 {
				http.Error(w, "this shared point has multiple connections; select a connection with link_id", 409)
				return
			}
		}
		e := db.QueryRow(query, args...).Scan(&id, &source, &target, &data)
		if e != nil {
			if e != sql.ErrNoRows {
				http.Error(w, "connection lookup failed", 500)
				return
			}
			if len(segments) == 0 {
				http.Error(w, "interface is not connected", 404)
				return
			}
			var openDevice, openKind string
			if db.QueryRow(`SELECT i.device_id,o.kind FROM interfaces i JOIN objects o ON o.id=i.device_id WHERE i.id=?`, current).Scan(&openDevice, &openKind) == nil && openKind == "patch_panel" {
				http.Error(w, "cable trace ends at an unconnected patch-panel port", 409)
				return
			}
			destination = current
			break
		}
		other := source
		if source == current {
			other = target
		}
		rrows, e := db.Query(`SELECT o.id,o.kind,o.name FROM cable_routes cr JOIN objects o ON o.id=cr.infrastructure_id WHERE cr.link_id=? ORDER BY cr.sequence`, id)
		if e != nil {
			http.Error(w, "route lookup failed", 500)
			return
		}
		route := []map[string]string{}
		for rrows.Next() {
			var oid, kind, name string
			rrows.Scan(&oid, &kind, &name)
			route = append(route, map[string]string{"id": oid, "kind": kind, "name": name})
		}
		rrows.Close()
		segments = append(segments, segment{id, current, other, route})
		var device string
		if db.QueryRow(`SELECT device_id FROM interfaces WHERE id=?`, other).Scan(&device) != nil {
			http.Error(w, "cable endpoint interface missing", 409)
			return
		}
		var kind string
		_ = db.QueryRow(`SELECT kind FROM objects WHERE id=?`, device).Scan(&kind)
		if kind != "patch_panel" || jsonClass(data) != "physical" {
			destination = other
			break
		}
		var paired string
		e = db.QueryRow(`SELECT CASE WHEN port_a=? THEN port_b ELSE port_a END FROM patch_pairings WHERE port_a=? OR port_b=?`, other, other, other).Scan(&paired)
		if e != nil {
			http.Error(w, "unpaired patch-panel port", 409)
			return
		}
		current = paired
	}
	if destination == "" {
		http.Error(w, "trace exceeded 64 cable segments", 409)
		return
	}
	var dev, name string
	_ = db.QueryRow(`SELECT device_id,name FROM interfaces WHERE id=?`, destination).Scan(&dev, &name)
	json.NewEncoder(w).Encode(map[string]any{"start_interface": start, "destination_interface": destination, "destination_device": dev, "destination_name": name, "segments": segments})
}

// Layout positions are shared by grouping mode while viewport state stays local.
func (a *App) layout(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	group := r.URL.Query().Get("grouping")
	if group == "" {
		http.Error(w, "grouping required", 400)
		return
	}
	if r.Method == "GET" {
		limit := 2000
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			limit = n
		}
		if limit > 5000 {
			limit = 5000
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		bound := func(key string) any {
			value := r.URL.Query().Get(key)
			if value == "" {
				return nil
			}
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil
			}
			return number
		}
		minx, maxx, miny, maxy := bound("min_x"), bound("max_x"), bound("min_y"), bound("max_y")
		rows, e := db.Query(`SELECT object_id,x,y FROM layouts WHERE grouping=? AND (? IS NULL OR x>=?) AND (? IS NULL OR x<=?) AND (? IS NULL OR y>=?) AND (? IS NULL OR y<=?) ORDER BY object_id LIMIT ? OFFSET ?`, group, minx, minx, maxx, maxx, miny, miny, maxy, maxy, limit, offset)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id string
			var x, y float64
			rows.Scan(&id, &x, &y)
			out = append(out, map[string]any{"id": id, "x": x, "y": y})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "forbidden", 403)
		return
	}
	var x struct {
		Positions []struct {
			ID string  `json:"id"`
			X  float64 `json:"x"`
			Y  float64 `json:"y"`
		} `json:"positions"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if json.NewDecoder(r.Body).Decode(&x) != nil {
		http.Error(w, "invalid positions", 400)
		return
	}
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	if x.ExpectedVersion != nil {
		var version int64
		if e = tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM audit WHERE action='layout_update' AND object_id=?`, group).Scan(&version); e != nil {
			http.Error(w, "layout version failed", 500)
			return
		}
		if version != *x.ExpectedVersion {
			http.Error(w, "another editor moved items in this view; reload before moving them", 409)
			return
		}
	}

	var before []map[string]any
	for _, v := range x.Positions {
		var px, py float64
		if tx.QueryRow(`SELECT x,y FROM layouts WHERE grouping=? AND object_id=?`, group, v.ID).Scan(&px, &py) == nil {
			before = append(before, map[string]any{"id": v.ID, "x": px, "y": py})
		}
	}
	for _, v := range x.Positions {
		if _, e = tx.Exec(`INSERT INTO layouts VALUES(?,?,?,?) ON CONFLICT(grouping,object_id) DO UPDATE SET x=excluded.x,y=excluded.y`, group, v.ID, v.X, v.Y); e != nil {
			http.Error(w, "layout save failed", 400)
			return
		}
	}
	if e = a.recordAudit(tx, p.User, "layout_update", group, map[string]any{"before": before, "after": x.Positions}); e != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]any{"type": "layout", "grouping": group})
	seq, e := appendEvent(tx, event)
	if e != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if e = a.commitLogged(tx); e != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(204)
}
func (a *App) broadcast(b []byte) {
	encoded, err := zstdMessage(b)
	if err != nil {
		log.Printf("WebSocket Zstandard compression failed: %v", err)
		return
	}
	b = encoded
	a.mu.RLock()
	dropped := 0
	for c := range a.clients {
		select {
		case c <- b:
		default:
			dropped++
		}
	}
	a.mu.RUnlock()
	if dropped > 0 {
		a.applicationLog("warn", "WebSocket notifications dropped; clients will replay events", map[string]string{"clients": strconv.Itoa(dropped)})
	}
}
func appendEvent(tx *sql.Tx, payload []byte) (int64, error) {
	res, e := tx.Exec(`INSERT INTO events(payload,created) VALUES(?,?)`, string(payload), time.Now().Unix())
	if e != nil {
		return 0, e
	}
	return res.LastInsertId()
}
func (a *App) notify(seq int64, payload []byte) {
	msg, _ := json.Marshal(map[string]any{"seq": seq, "payload": json.RawMessage(payload)})
	a.broadcast(msg)
}
func (a *App) events(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	rows, e := db.Query(`SELECT seq,payload FROM events WHERE seq>? ORDER BY seq LIMIT 2000`, after)
	if e != nil {
		http.Error(w, "event query failed", 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var raw string
		if rows.Scan(&seq, &raw) == nil {
			out = append(out, map[string]any{"seq": seq, "payload": json.RawMessage(raw)})
		}
	}
	json.NewEncoder(w).Encode(out)
}

// Links saved before connection classes were introduced are physical.
func jsonClass(data string) string {
	var spec map[string]any
	_ = json.Unmarshal([]byte(data), &spec)
	if cls, _ := spec["connection_class"].(string); cls != "" {
		return cls
	}
	return "physical"
}
