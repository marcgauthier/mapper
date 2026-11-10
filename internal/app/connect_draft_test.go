package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func draftOwnerVersion(t *testing.T, a *App, id string) int {
	t.Helper()
	var v int
	if err := a.db.QueryRow(`SELECT version FROM objects WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func draftPorts(t *testing.T, a *App, id string) []map[string]string {
	t.Helper()
	w := apiRequest(a, "GET", "/api/interfaces?device_id="+id, "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ports []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &ports); err != nil {
		t.Fatal(err)
	}
	return ports
}
func postDraft(a *App, source, target draftEndpoint, sid string) (int, string) {
	body, _ := json.Marshal(map[string]any{"id": newID(), "source": source, "target": target, "data": map[string]any{"connection_class": "physical", "physical_type": "copper_twisted_pair", "cable_type": "Cat6", "connector_a": "RJ45", "connector_b": "RJ45"}})
	w := apiRequest(a, http.MethodPost, "/api/topology/connect", string(body), sid)
	return w.Code, w.Body.String()
}
func TestConnectionDraftDVOPlacementAndAtomicCreation(t *testing.T) {
	for _, container := range []string{"s1", "b1", "f2", "r1", "r2", "rack"} {
		t.Run(container, func(t *testing.T) {
			a := topologyFixture(t)
			ports := draftPorts(t, a, "d3")
			source := draftEndpoint{ContainerID: container, ProvisionalID: "new-dvo", Version: draftOwnerVersion(t, a, container), InterfaceName: "Data"}
			target := draftEndpoint{ObjectID: "d3", Version: draftOwnerVersion(t, a, "d3"), InterfaceID: ports[0]["id"], Revision: ports[0]["revision"]}
			code, body := postDraft(a, source, target, "s")
			if code != 200 {
				t.Fatal(code, body)
			}
			var raw string
			if err := a.db.QueryRow(`SELECT data FROM objects WHERE id='new-dvo'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(raw), &meta); err != nil {
				t.Fatal(err)
			}
			if meta["outlet_type"] != "dvo" {
				t.Fatal(raw)
			}
			if container == "s1" && meta["building_id"] != nil {
				t.Fatal("invented building", raw)
			}
			if container == "b1" && meta["floor_id"] != nil {
				t.Fatal("invented floor", raw)
			}
			var kind string
			if err := a.db.QueryRow(`SELECT kind FROM objects WHERE id=?`, container).Scan(&kind); err != nil {
				t.Fatal(err)
			}
			page := topologyPage(t, a, kind, container)
			if !hasTopologyNode(page, "new-dvo") {
				t.Fatal("DVO missing from its parent view", container, page.Nodes)
			}
			if len(draftPorts(t, a, "new-dvo")) != 2 {
				t.Fatal("DVO did not receive Data/Voice ports")
			}
			var links, events int
			a.db.QueryRow(`SELECT count(*) FROM links`).Scan(&links)
			a.db.QueryRow(`SELECT count(*) FROM events WHERE payload LIKE '%connection_create%'`).Scan(&events)
			if links != 1 || events != 1 {
				t.Fatal(links, events)
			}
		})
	}
}
func TestConnectionDraftRollbackAndConcurrentInterfaces(t *testing.T) {
	a := topologyFixture(t)
	if _, err := a.db.Exec(`DELETE FROM interfaces WHERE device_id='d2'`); err != nil {
		t.Fatal(err)
	}
	source := draftEndpoint{ContainerID: "b1", ProvisionalID: "rollback-dvo", Version: draftOwnerVersion(t, a, "b1")}
	target := draftEndpoint{ObjectID: "d2", AutoInterface: true, Version: draftOwnerVersion(t, a, "d2")}
	// Occupy an interface while the form expects a zero-interface owner.
	if _, err := a.db.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES('concurrent','d2','Other user','{}')`); err != nil {
		t.Fatal(err)
	}
	code, body := postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM objects WHERE id='rollback-dvo'`).Scan(&n)
	if n != 0 {
		t.Fatal("DVO escaped rollback")
	}
	a.db.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id='rollback-dvo'`).Scan(&n)
	if n != 0 {
		t.Fatal("DVO ports escaped rollback")
	}
	if _, err := a.db.Exec(`DELETE FROM interfaces WHERE device_id='d2'`); err != nil {
		t.Fatal(err)
	}
	code, body = postDraft(a, source, target, "s")
	if code != 200 {
		t.Fatal(code, body)
	}
	ports := draftPorts(t, a, "d2")
	if len(ports) != 1 || ports[0]["name"] != "Interface 1" {
		t.Fatal(ports)
	}
}
func TestConnectionDraftConflictsAndRoles(t *testing.T) {
	a := topologyFixture(t)
	ports := draftPorts(t, a, "d2")
	source := draftEndpoint{ContainerID: "r1", Version: draftOwnerVersion(t, a, "r1"), ProvisionalID: "draft-outlet"}
	target := draftEndpoint{ObjectID: "d2", Version: draftOwnerVersion(t, a, "d2"), InterfaceID: ports[0]["id"], Revision: ports[0]["revision"]}
	source.Version++
	code, body := postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	source.Version--
	target.InterfaceID = "missing"
	code, body = postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	target.InterfaceID = ports[0]["id"]
	target.Revision = "stale"
	code, body = postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	target.Revision = ports[0]["revision"]
	code, body = postDraft(a, source, target, "")
	if code != 401 {
		t.Fatal(code, body)
	}
	a.mu.Lock()
	p := a.sessions["s"]
	p.Role = "viewer"
	a.sessions["s"] = p
	a.mu.Unlock()
	code, body = postDraft(a, source, target, "s")
	if code != 403 {
		t.Fatal(code, body)
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM objects WHERE id='draft-outlet'`).Scan(&n)
	if n != 0 {
		t.Fatal("failed requests created DVO")
	}
}
func TestDirectDVOCSVRoundTrip(t *testing.T) {
	a := topologyFixture(t)
	for _, parent := range []struct{ key, id string }{{"site_id", "s1"}, {"building_id", "b1"}, {"rack_id", "rack"}} {
		topologyPost(t, a, fmt.Sprintf(`{"id":"dvo-%s","kind":"outlet","name":"Direct DVO","outlet_type":"dvo",%q:%q}`, parent.id, parent.key, parent.id))
	}
	exported := apiRequest(a, "GET", "/api/export", "", "s")
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body.String())
	}
	target, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		w := apiRequest(target, "POST", path, exported.Body.String(), "s")
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, c := range []struct{ kind, id string }{{"site", "s1"}, {"building", "b1"}, {"rack", "rack"}} {
		if !hasTopologyNode(topologyPage(t, target, c.kind, c.id), "dvo-"+c.id) {
			t.Fatal("placement lost", c)
		}
	}
}

func TestConnectionDraftOccupiedTargetRollsBackGeneratedRecords(t *testing.T) {
	a := topologyFixture(t)
	p1, p2 := draftPorts(t, a, "d1")[0]["id"], draftPorts(t, a, "d2")[0]["id"]
	if _, err := a.db.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES('occupied',?,?,'{"connection_class":"physical","physical_type":"backplane"}')`, p1, p2); err != nil {
		t.Fatal(err)
	}
	source := draftEndpoint{ContainerID: "b2", ProvisionalID: "never-created", Version: draftOwnerVersion(t, a, "b2")}
	target := draftEndpoint{ObjectID: "d2", Version: draftOwnerVersion(t, a, "d2"), InterfaceID: p2}
	var audits, events int
	a.db.QueryRow(`SELECT count(*) FROM audit`).Scan(&audits)
	a.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	code, body := postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	var count int
	a.db.QueryRow(`SELECT count(*) FROM objects WHERE id='never-created'`).Scan(&count)
	if count != 0 {
		t.Fatal("orphaned DVO")
	}
	a.db.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id='never-created'`).Scan(&count)
	if count != 0 {
		t.Fatal("orphaned ports")
	}
	var afterAudits, afterEvents int
	a.db.QueryRow(`SELECT count(*) FROM audit`).Scan(&afterAudits)
	a.db.QueryRow(`SELECT count(*) FROM events`).Scan(&afterEvents)
	if audits != afterAudits || events != afterEvents {
		t.Fatal("failed connection published audit/events")
	}
	target.ObjectID = "removed"
	code, body = postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
	source.ContainerID = "removed"
	code, body = postDraft(a, source, target, "s")
	if code != 409 {
		t.Fatal(code, body)
	}
}
