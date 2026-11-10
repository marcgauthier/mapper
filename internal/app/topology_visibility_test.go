package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func withoutFixtureConduits(t *testing.T, a *App) {
	t.Helper()
	for _, id := range []string{"c1", "c2"} {
		w := apiRequest(a, http.MethodDelete, "/api/objects?id="+id, "", "s")
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func assertTopologyEndpoints(t *testing.T, page topologyTestPage) {
	t.Helper()
	for _, edge := range page.Edges {
		if !hasTopologyNode(page, edge.Source) || !hasTopologyNode(page, edge.Target) {
			t.Fatal("dangling edge", edge)
		}
	}
	for _, node := range page.Nodes {
		if node.Parent != "" && !hasTopologyNode(page, node.Parent) {
			t.Fatal("missing compound parent", node)
		}
	}
}
func TestFocusedLocationsKeepChildrenAndHideUnconnectedMapReferences(t *testing.T) {
	a := topologyFixture(t)
	withoutFixtureConduits(t, a)
	topologyPost(t, a, `{"id":"empty-room","kind":"room","name":"Empty room","floor_id":"f2"}`)
	topologyPost(t, a, `{"id":"empty-rack","kind":"rack","name":"Empty rack","telecom_room_id":"r1"}`)
	for _, c := range []struct {
		kind, id        string
		present, absent []string
	}{
		{"site", "s1", []string{"b1", "b2", "generator", "building-ups"}, []string{"s1", "s2"}},
		{"building", "b1", []string{"ground:b1", "f2", "r1", "r2", "empty-room", "generator", "building-ups"}, []string{"b1", "b2", "s1", "s2"}},
		{"floor", "f2", []string{"r2", "empty-room"}, []string{"f2", "b1", "ground:b1", "r1"}},
		{"room", "r2", []string{"d2", "room-ups"}, []string{"r2", "f2", "b1", "r1", "rack"}},
		{"telecom_room", "r1", []string{"rack", "empty-rack", "telecom-ups"}, []string{"r1", "ground:b1", "b1", "r2"}},
		{"rack", "rack", []string{"d1"}, []string{"rack", "r1", "ground:b1", "b1"}},
	} {
		t.Run(c.kind, func(t *testing.T) {
			page := topologyPage(t, a, c.kind, c.id)
			for _, id := range c.present {
				if !hasTopologyNode(page, id) {
					t.Fatal("owned inventory missing", id, page.Nodes)
				}
			}
			for _, id := range c.absent {
				if hasTopologyNode(page, id) {
					t.Fatal("unconnected map reference visible", id, page.Nodes)
				}
			}
			assertTopologyEndpoints(t, page)
		})
	}
}
func TestPhysicalConnectionControlsNeighborVisibility(t *testing.T) {
	a := topologyFixture(t)
	withoutFixtureConduits(t, a)
	var source, target string
	if e := a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d2'`).Scan(&source); e != nil {
		t.Fatal(e)
	}
	if e := a.db.QueryRow(`SELECT id FROM interfaces WHERE device_id='d3'`).Scan(&target); e != nil {
		t.Fatal(e)
	}
	payload := fmt.Sprintf(`{"id":"physical-neighbor","source_interface":%q,"target_interface":%q,"data":{"connection_class":"physical","physical_type":"fiber","cable_type":"OM4","connector_a":"LC","connector_b":"LC"}}`, source, target)
	w := apiRequest(a, http.MethodPost, "/api/links", payload, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, c := range []struct{ kind, id string }{{"building", "b1"}, {"room", "r2"}} {
		page := topologyPage(t, a, c.kind, c.id)
		if !hasTopologyNode(page, "b2") {
			t.Fatal("connected neighbor hidden", c, page.Nodes)
		}
		assertTopologyEndpoints(t, page)
	}
	// A logical-only link must not introduce a physically unconnected location.
	payload = fmt.Sprintf(`{"id":"logical-neighbor","source_interface":%q,"target_interface":%q,"data":{"connection_class":"virtual","virtual_type":"ethernet"}}`, source, target)
	w = apiRequest(a, http.MethodPost, "/api/links", payload, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, http.MethodDelete, "/api/links?id=physical-neighbor", "", "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, c := range []struct{ kind, id string }{{"building", "b1"}, {"room", "r2"}} {
		page := topologyPage(t, a, c.kind, c.id)
		if hasTopologyNode(page, "b2") {
			t.Fatal("logical-only neighbor retained", c, page.Nodes)
		}
		assertTopologyEndpoints(t, page)
	}
}
func TestLocationRelationshipsCannotIntroduceUnconnectedPeer(t *testing.T) {
	a := topologyFixture(t)
	withoutFixtureConduits(t, a)
	w := apiRequest(a, http.MethodPost, "/api/relationships", `{"source_id":"b1","target_id":"b2","relationship_type":"depends_on"}`, "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	page := topologyPage(t, a, "building", "b1")
	if hasTopologyNode(page, "b2") || hasTopologyNode(page, "b1") {
		t.Fatal(page.Nodes)
	}
	assertTopologyEndpoints(t, page)
	// Child maps and disconnected equipment still appear in paginated responses.
	w = apiRequest(a, http.MethodGet, "/api/topology/view?scope_kind=building&scope_id=b1&limit=1", "", "s")
	var first topologyTestPage
	if e := json.Unmarshal(w.Body.Bytes(), &first); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || first.Next == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	assertTopologyEndpoints(t, first)
}

func TestExternalMapsArriveWithTheirConnections(t *testing.T) {
	a := topologyFixture(t)
	cursor := ""
	seenNeighbor := false
	for pages := 0; pages < 100; pages++ {
		path := "/api/topology/view?scope_kind=building&scope_id=b1&limit=1&after=" + cursor
		w := apiRequest(a, http.MethodGet, path, "", "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page topologyTestPage
		if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		for _, node := range page.Nodes {
			if !node.Boundary && node.ID != "b1" {
				continue
			}
			incident := false
			for _, edge := range page.Edges {
				if edge.Source == node.ID || edge.Target == node.ID {
					incident = true
				}
			}
			if !incident {
				t.Fatal("external map arrived without its connection", node, page)
			}
			if node.ID == "b2" {
				seenNeighbor = true
			}
		}
		assertTopologyEndpoints(t, page)
		if page.Next == "" {
			if !seenNeighbor {
				t.Fatal("connected building never appeared")
			}
			return
		}
		cursor = page.Next
	}
	t.Fatal("pagination did not terminate")
}
