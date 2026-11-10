package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func topologyPost(t *testing.T, a *App, body string) {
	t.Helper()
	w := apiRequest(a, http.MethodPost, "/api/objects", body, "s")
	if w.Code != 204 {
		t.Fatalf("create %s: %d %s", body, w.Code, w.Body.String())
	}
}
func topologyFixture(t *testing.T) *App {
	t.Helper()
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"s1","kind":"site","name":"Campus","site_role":"major_site"}`, `{"id":"s2","kind":"site","name":"Branch","site_role":"branch_office"}`,
		`{"id":"b1","kind":"building","name":"Building A","site_id":"s1"}`, `{"id":"b2","kind":"building","name":"Building B","site_id":"s1"}`,
		`{"id":"f2","kind":"floor","name":"Upper","building_id":"b1"}`,
		`{"id":"r1","kind":"telecom_room","name":"MDF","floor_id":"ground:b1"}`, `{"id":"r2","kind":"room","name":"Office","floor_id":"f2"}`,
		`{"id":"rack","kind":"rack","name":"Rack","telecom_room_id":"r1"}`,
		`{"id":"d1","kind":"device","name":"Gateway","device_type_id":"switch","rack_id":"rack"}`, `{"id":"d2","kind":"device","name":"Office switch","device_type_id":"switch","room_id":"r2"}`, `{"id":"d3","kind":"device","name":"Building B switch","device_type_id":"switch","building_id":"b2"}`,
		`{"id":"c1","kind":"conduit","name":"Riser","endpoint_a_id":"r1","endpoint_b_id":"r2"}`, `{"id":"c2","kind":"conduit","name":"Campus duct","endpoint_a_id":"b1","endpoint_b_id":"b2"}`,
		`{"id":"internet","kind":"service","name":"Internet A","service_category":"internet"}`, `{"id":"aws","kind":"service","name":"AWS account","service_category":"cloud_provider","provider":"AWS"}`, `{"id":"crm","kind":"service","name":"CRM","service_category":"saas"}`,
		`{"id":"generator","kind":"generator","name":"Generator A","building_id":"b1"}`, `{"id":"building-ups","kind":"ups","name":"Building UPS","building_id":"b1"}`, `{"id":"room-ups","kind":"ups","name":"Office UPS","room_id":"r2"}`, `{"id":"telecom-ups","kind":"ups","name":"MDF UPS","telecom_room_id":"r1"}`,
	} {
		topologyPost(t, a, body)
	}
	return a
}

type topologyTestPage struct {
	Nodes     []topologyNode `json:"nodes"`
	Edges     []topologyEdge `json:"edges"`
	Next      string         `json:"next_cursor"`
	Sequence  int64          `json:"sequence"`
	Actions   []string       `json:"allowed_creation_kinds"`
	Positions map[string]struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"positions"`
}

func topologyPage(t *testing.T, a *App, kind, id string) topologyTestPage {
	t.Helper()
	w := apiRequest(a, http.MethodGet, "/api/topology/view?scope_kind="+kind+"&scope_id="+url.QueryEscape(id)+"&limit=500", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p topologyTestPage
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	return p
}
func hasTopologyNode(p topologyTestPage, id string) bool {
	for _, n := range p.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}
func TestTopologyScopesConduitsAndPower(t *testing.T) {
	a := topologyFixture(t)
	global := topologyPage(t, a, "global", "")
	for _, id := range []string{"s1", "s2", "aws", "crm", "internet"} {
		if !hasTopologyNode(global, id) {
			t.Fatal("global missing", id)
		}
	}
	if hasTopologyNode(global, "d1") || hasTopologyNode(global, "generator") {
		t.Fatal("global leaks equipment")
	}
	site := topologyPage(t, a, "site", "s1")
	for _, id := range []string{"b1", "b2", "generator", "building-ups"} {
		if !hasTopologyNode(site, id) {
			t.Fatal("site missing", id)
		}
	}
	if hasTopologyNode(site, "room-ups") || hasTopologyNode(site, "telecom-ups") {
		t.Fatal("site leaks room UPS")
	}
	found := false
	for _, e := range site.Edges {
		if e.ObjectID == "c2" && e.Source != e.Target {
			found = true
		}
	}
	if !found {
		t.Fatal("building conduit missing")
	}
	building := topologyPage(t, a, "building", "b1")
	for _, id := range []string{"ground:b1", "f2", "r1", "r2", "generator", "building-ups"} {
		if !hasTopologyNode(building, id) {
			t.Fatal("building missing", id)
		}
	}
	found = false
	for _, e := range building.Edges {
		if e.ObjectID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatal("riser not visible in building")
	}
	floor := topologyPage(t, a, "floor", "ground:b1")
	found = false
	for _, n := range floor.Nodes {
		if n.ID == "f2" && n.Boundary {
			found = true
		}
	}
	if !found {
		t.Fatal("cross floor destination must be a boundary")
	}
	room := topologyPage(t, a, "room", "r2")
	if !hasTopologyNode(room, "room-ups") || hasTopologyNode(room, "building-ups") {
		t.Fatal("room UPS visibility")
	}
	telecom := topologyPage(t, a, "telecom_room", "r1")
	if !hasTopologyNode(telecom, "telecom-ups") {
		t.Fatal("telecom UPS missing")
	}
	rack := topologyPage(t, a, "rack", "rack")
	if !hasTopologyNode(rack, "d1") {
		t.Fatal("rack equipment missing")
	}
	for _, p := range []topologyTestPage{site, building, room, telecom} {
		for _, e := range p.Edges {
			if e.Category == "power" && (!hasTopologyNode(p, e.Source) || !hasTopologyNode(p, e.Target)) {
				t.Fatal("dangling power edge")
			}
		}
	}
}
func TestTopologyRelationshipsAndWAN(t *testing.T) {
	a := topologyFixture(t)
	for _, body := range []string{`{"id":"access","source_id":"s1","target_id":"crm","relationship_type":"accesses"}`, `{"id":"hosts","source_id":"aws","target_id":"crm","relationship_type":"hosts"}`} {
		w := apiRequest(a, http.MethodPost, "/api/relationships", body, "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := apiRequest(a, http.MethodPost, "/api/relationships", `{"source_id":"crm","target_id":"aws","relationship_type":"hosts"}`, "s")
	if w.Code != 400 {
		t.Fatal("hosting cycle accepted", w.Code)
	}
	var port string
	_ = a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d1'`).Scan(&port)
	w = apiRequest(a, http.MethodPost, "/api/topology/attachment", fmt.Sprintf(`{"gateway_interface":%q,"service_id":"internet","circuit_id":"Circuit 1"}`, port), "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	global := topologyPage(t, a, "global", "")
	if len(global.Edges) != 3 {
		t.Fatalf("expected access, hosting and WAN edges: %+v", global.Edges)
	}
	cloud := topologyPage(t, a, "service", "aws")
	if !hasTopologyNode(cloud, "crm") {
		t.Fatal("hosted SaaS missing")
	}
	w = apiRequest(a, http.MethodDelete, "/api/objects?id=crm", "", "s")
	if w.Code != 409 {
		t.Fatal("service references not protected", w.Code)
	}
}
func TestPLSTransportServiceAndAttachment(t *testing.T) {
	a := topologyFixture(t)
	topologyPost(t, a, `{"id":"pls1","kind":"service","name":"PLS WAN","service_category":"pls"}`)
	var port string
	_ = a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d1'`).Scan(&port)
	w := apiRequest(a, http.MethodPost, "/api/topology/attachment", fmt.Sprintf(`{"gateway_interface":%q,"service_id":"pls1","circuit_id":"PLS-1"}`, port), "s")
	if w.Code != 200 {
		t.Fatalf("pls attach: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &created); e != nil || created.ID == "" {
		t.Fatalf("attachment id: %s %v", w.Body.String(), e)
	}
	var class, stype string
	if e := a.db.QueryRow(`SELECT COALESCE(json_extract(data,'$.connection_class'),''),COALESCE(json_extract(data,'$.service_type'),'') FROM links WHERE id=?`, created.ID).Scan(&class, &stype); e != nil {
		t.Fatal(e)
	}
	if class != "provider_service" || stype != "pls" {
		t.Fatalf("unexpected link %s/%s", class, stype)
	}
	if site := topologyPage(t, a, "site", "s1"); !hasTopologyNode(site, "pls1") {
		t.Fatal("pls transport missing from site view")
	}
	global := topologyPage(t, a, "global", "")
	pls, okPls := global.Positions["pls1"]
	saas, okSaas := global.Positions["crm"]
	if !okPls || !okSaas || pls.X >= saas.X {
		t.Fatalf("pls not in WAN lane: pls=%v saas=%v", pls, saas)
	}
	w = apiRequest(a, http.MethodPost, "/api/topology/attachment", fmt.Sprintf(`{"gateway_interface":%q,"service_id":"crm"}`, port), "s")
	if w.Code != 400 {
		t.Fatalf("non-WAN attachment accepted: %d", w.Code)
	}
	var plsPort string
	_ = a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='pls1'`).Scan(&plsPort)
	w = apiRequest(a, http.MethodPost, "/api/links", fmt.Sprintf(`{"source_interface":%q,"target_interface":%q,"data":{"connection_class":"provider_service","service_type":"mpls"}}`, port, plsPort), "s")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "PLS") {
		t.Fatalf("mismatched pls link accepted: %d %s", w.Code, w.Body.String())
	}
	exported := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	target, targetDB := editorApp(t, "editor")
	if err := migrateSchema(targetDB); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"/api/import?dry_run=1", "/api/import"} {
		w = apiRequest(target, http.MethodPost, url, exported.Body.String(), "s")
		if w.Code != 200 {
			t.Fatal(url, w.Code, w.Body.String())
		}
	}
	var category string
	if e := targetDB.QueryRow(`SELECT json_extract(data,'$.service_category') FROM objects WHERE id='pls1'`).Scan(&category); e != nil || category != "pls" {
		t.Fatalf("pls lost in CSV round trip: %q %v", category, e)
	}
}
func TestTopologyGroundMigrationAndPagination(t *testing.T) {
	a, db := editorApp(t, "editor")
	if _, e := db.Exec(`INSERT INTO objects VALUES('b','building','Legacy','{"site_id":"s"}',0,0,1),('d','device','Legacy device','{"building_id":"b"}',12,34,1)`); e != nil {
		t.Fatal(e)
	}
	if e := migrateSchema(db); e != nil {
		t.Fatal(e)
	}
	var raw string
	if e := db.QueryRow(`SELECT data FROM objects WHERE id='d'`).Scan(&raw); e != nil || !strings.Contains(raw, "ground:b") {
		t.Fatal(raw, e)
	}
	if e := migrateSchema(db); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		topologyPost(t, a, fmt.Sprintf(`{"id":"site%d","kind":"site","name":"Site %d","site_role":"branch_office"}`, i, i))
	}
	w := apiRequest(a, http.MethodGet, "/api/topology/view?limit=2", "", "s")
	var page topologyTestPage
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || len(page.Nodes) != 2 || page.Next == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	topologyPost(t, a, `{"id":"new","kind":"site","name":"New","site_role":"branch_office"}`)
	w = apiRequest(a, http.MethodGet, fmt.Sprintf("/api/topology/view?limit=2&after=%s&sequence=%d", page.Next, page.Sequence), "", "s")
	if w.Code != 409 {
		t.Fatal("stale pagination accepted", w.Code)
	}
}
func TestTopologyPlacementValidationAndRoles(t *testing.T) {
	a := topologyFixture(t)
	for _, body := range []string{`{"kind":"generator","name":"Bad","room_id":"r2"}`, `{"kind":"ups","name":"Bad"}`, `{"kind":"room","name":"Bad"}`, `{"kind":"floor","name":"Bad"}`, `{"kind":"service","name":"Bad","service_category":"invalid"}`} {
		w := apiRequest(a, http.MethodPost, "/api/objects", body, "s")
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	topologyPost(t, a, `{"id":"dvo","kind":"outlet","name":"Wall DVO","outlet_type":"dvo","room_id":"r2"}`)
	var n int
	_ = a.db.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id='dvo'`).Scan(&n)
	if n != 2 {
		t.Fatal("DVO ports", n)
	}
	a.mu.Lock()
	a.sessions["s"] = principal{User: "viewer", Role: "viewer"}
	a.mu.Unlock()
	p := topologyPage(t, a, "site", "s1")
	if len(p.Actions) != 0 {
		t.Fatal("viewer creation actions")
	}
	w := apiRequest(a, http.MethodPost, "/api/relationships", `{"source_id":"s1","target_id":"crm","relationship_type":"accesses"}`, "s")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestSiteRoleRequiredAndValidated(t *testing.T) {
	a := topologyFixture(t)
	for _, body := range []string{`{"kind":"site","name":"No role"}`, `{"kind":"site","name":"Bad role","site_role":"mothership"}`} {
		w := apiRequest(a, http.MethodPost, "/api/objects", body, "s")
		if w.Code != 400 {
			t.Fatalf("%s: got %d", body, w.Code)
		}
	}
	for _, role := range []string{"data_center", "major_site", "branch_office", "edge_site", "disaster_recovery", "cloud_colo"} {
		topologyPost(t, a, fmt.Sprintf(`{"id":"role-%s","kind":"site","name":"Role %s","site_role":%q}`, role, role, role))
	}
	// Shared validation paths stay tolerant of legacy role-less sites (undo/import).
	func() {
		tx, e := a.db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e := normalizeTopologyPlacement(tx, "legacy", "site", map[string]any{"name": "Legacy"}); e != nil {
			t.Fatalf("legacy site rejected: %v", e)
		}
		if e := normalizeTopologyPlacement(tx, "legacy", "site", map[string]any{"name": "Legacy", "site_role": "mothership"}); e == nil {
			t.Fatal("unknown site role accepted")
		}
	}()
	// Legacy CSV without a role column still imports.
	w := apiRequest(a, http.MethodPost, "/api/import", "kind,id,name\nsite,legacy-csv,Legacy CSV\n", "s")
	if w.Code != 200 {
		t.Fatalf("legacy import: %d %s", w.Code, w.Body.String())
	}
	var role string
	_ = a.db.QueryRow(`SELECT COALESCE(json_extract(data,'$.site_role'),'') FROM objects WHERE id='role-branch_office'`).Scan(&role)
	if role != "branch_office" {
		t.Fatalf("role not stored: %q", role)
	}
}

func TestTopologyRoutesConversionAndCSV(t *testing.T) {
	a := topologyFixture(t)
	topologyPost(t, a, `{"id":"outlet","kind":"outlet","name":"Fiber outlet","outlet_type":"fiber","room_id":"r2"}`)
	var aPort, zPort string
	_ = a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d1'`).Scan(&aPort)
	_ = a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d2'`).Scan(&zPort)
	body := fmt.Sprintf(`{"id":"riser-link","source_interface":%q,"target_interface":%q,"conduit_id":"c1","data":{"medium":"fiber","cable_type":"OS2","connector_a":"LC","connector_b":"LC","strand_count":24}}`, aPort, zPort)
	w := apiRequest(a, http.MethodPost, "/api/links", body, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := topologyPage(t, a, "conduit", "c1")
	if !hasTopologyNode(p, "d1") || !hasTopologyNode(p, "d2") {
		t.Fatal("conduit cable endpoints missing")
	}
	w = apiRequest(a, http.MethodPost, "/api/routes", `{"link_id":"riser-link","infrastructure_ids":["c2"],"expected_infrastructure_ids":[]}`, "s")
	if w.Code != 409 {
		t.Fatal("stale route accepted", w.Code)
	}
	w = apiRequest(a, http.MethodPost, "/api/routes", `{"link_id":"riser-link","infrastructure_ids":["c1","c2"],"expected_infrastructure_ids":["c1"]}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	topologyPost(t, a, `{"id":"legacy-cloud","kind":"device","name":"Legacy AWS","device_type_id":"aws","building_id":"b1"}`)
	var version int
	_ = a.db.QueryRow(`SELECT version FROM objects WHERE id='legacy-cloud'`).Scan(&version)
	w = apiRequest(a, http.MethodPost, "/api/topology/convert-service", fmt.Sprintf(`{"id":"legacy-cloud","version":%d,"service_category":"cloud_provider"}`, version), "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPost, "/api/relationships", `{"id":"rel","source_id":"s1","target_id":"crm","relationship_type":"accesses"}`, "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	exported := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	target, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"/api/import?dry_run=1", "/api/import"} {
		w = apiRequest(target, http.MethodPost, url, exported.Body.String(), "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM relationships WHERE id='rel'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if !hasTopologyNode(topologyPage(t, target, "site", "s1"), "generator") {
		t.Fatal("generator lost in export")
	}
}
func TestTopologyVersionedEditsAndUndo(t *testing.T) {
	a := topologyFixture(t)
	rel := `{"id":"rel","source_id":"s1","target_id":"crm","relationship_type":"accesses"}`
	w := apiRequest(a, http.MethodPost, "/api/relationships", rel, "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPost, "/api/relationships", rel, "s")
	if w.Code != 409 {
		t.Fatal("stale relationship accepted", w.Code)
	}
	w = apiRequest(a, http.MethodPost, "/api/relationships/undo", `{}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPost, "/api/relationships/undo", `{}`, "s")
	if w.Code != 409 {
		t.Fatal("double relationship undo accepted", w.Code)
	}
	var raw string
	var version int
	_ = a.db.QueryRow(`SELECT data,version FROM objects WHERE id='r2'`).Scan(&raw, &version)
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	m["floor_id"] = "ground:b1"
	m["version"] = version
	body, _ := json.Marshal(m)
	topologyPost(t, a, string(body))
	var data string
	_ = a.db.QueryRow(`SELECT data FROM objects WHERE id='room-ups'`).Scan(&data)
	if !strings.Contains(data, "ground:b1") {
		t.Fatal("UPS ancestry did not follow room", data)
	}
	w = apiRequest(a, http.MethodPost, "/api/undo", `{}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = a.db.QueryRow(`SELECT data FROM objects WHERE id='room-ups'`).Scan(&data)
	if !strings.Contains(data, `"floor_id":"f2"`) {
		t.Fatal("undo did not restore descendant ancestry", data)
	}
	w = apiRequest(a, http.MethodPost, "/api/layout?grouping=scope:site:s1", `{"positions":[{"id":"b1","x":10,"y":20}],"expected_version":0}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodPost, "/api/layout?grouping=scope:site:s1", `{"positions":[{"id":"b1","x":30,"y":40}],"expected_version":0}`, "s")
	if w.Code != 409 {
		t.Fatal("stale layout accepted", w.Code)
	}
}

func TestTopologyViewportAndRelationshipDetails(t *testing.T) {
	a := topologyFixture(t)
	w := apiRequest(a, http.MethodGet, "/api/topology/view?scope_kind=site&scope_id=s1&min_x=0&min_y=0&max_x=500&max_y=500&limit=500", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page topologyTestPage
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	for _, e := range page.Edges {
		if !hasTopologyNode(page, e.Source) || !hasTopologyNode(page, e.Target) {
			t.Fatal("viewport connection lost an endpoint")
		}
	}
	w = apiRequest(a, http.MethodGet, "/api/topology/view?min_x=NaN&min_y=0&max_x=100&max_y=100", "", "s")
	if w.Code != 400 {
		t.Fatal("nonfinite bounds accepted", w.Code)
	}
	for i := 0; i < 2; i++ {
		w = apiRequest(a, http.MethodPost, "/api/relationships", fmt.Sprintf(`{"id":"access%d","source_id":"s1","target_id":"crm","relationship_type":"accesses"}`, i), "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	overview := topologyPage(t, a, "global", "")
	var edge topologyEdge
	for _, e := range overview.Edges {
		if e.Category == "relationship:accesses" {
			edge = e
		}
	}
	if edge.Count != 2 {
		t.Fatal("summary count", edge)
	}
	w = apiRequest(a, http.MethodGet, "/api/topology/details?edge_id="+url.QueryEscape(edge.ID)+"&limit=1", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var details struct {
		Items []any  `json:"items"`
		Next  string `json:"next_cursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &details)
	if len(details.Items) != 1 || details.Next == "" {
		t.Fatal(w.Body.String())
	}
}
