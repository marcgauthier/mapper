package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func multiPort(t *testing.T, a *App, id, device, vrf string) {
	t.Helper()
	w := apiRequest(a, http.MethodPost, "/api/interfaces", `{"id":"`+id+`","device_id":"`+device+`","name":"`+id+`","data":{"speed":"1G","vrfs":["`+vrf+`"]}}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestVRFTraceTraversalAndIsolation(t *testing.T) {
	a := vrfFixture(t)
	multiPort(t, a, "start", "router", "Production")
	multiPort(t, a, "middle", "switch", "Production")
	multiPort(t, a, "finish", "switch", "Production")
	multiPort(t, a, "guest", "switch", "Guest")
	if _, err := a.db.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES('path','start','middle','{}')`); err != nil {
		t.Fatal(err)
	}
	a.sessions["viewer"] = principal{User: "viewer", Role: "viewer"}
	a.sessionExpires["viewer"] = time.Now().Add(time.Hour)
	cases := []struct {
		target, vrf string
		code        int
		reachable   bool
	}{{"finish", "Production", 200, true}, {"guest", "Production", 400, false}, {"finish", "Guest", 400, false}, {"absent", "Production", 404, false}, {"start", "Production", 200, true}}
	for _, c := range cases {
		w := apiRequest(a, "GET", "/api/trace?interface_id=start&target_interface_id="+c.target+"&vrf="+c.vrf, "", "viewer")
		if w.Code != c.code {
			t.Fatal(c, w.Code, w.Body.String())
		}
		if c.code != 200 {
			continue
		}
		var result struct {
			Reachable bool           `json:"reachable"`
			Steps     []vrfTraceStep `json:"steps"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Reachable != c.reachable {
			t.Fatal(result)
		}
		if c.target == "finish" && (len(result.Steps) != 2 || result.Steps[1].Kind != "device" || result.Steps[0].FromName == "") {
			t.Fatal(result)
		}
	}
	if _, err := a.db.Exec(`DELETE FROM links`); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(a, "GET", "/api/trace?interface_id=start&target_interface_id=finish&vrf=Production", "", "s")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reachable":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestVRFTraceTransparentPanels(t *testing.T) {
	a := vrfFixture(t)
	multiPort(t, a, "start", "router", "Production")
	multiPort(t, a, "finish", "switch", "Production")
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('panel','patch_panel','Panel','{}',0,0)`,
		`INSERT INTO interfaces(id,device_id,name,data) VALUES('pa','panel','a','{}'),('pb','panel','b','{}')`,
		`INSERT INTO links(id,source_interface,target_interface,data) VALUES('a','start','pa','{}'),('b','pb','finish','{}')`,
		`INSERT INTO patch_pairings(id,panel_id,port_a,port_b) VALUES('pair','panel','pa','pb')`,
	} {
		if _, e := a.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	w := apiRequest(a, "GET", "/api/trace?interface_id=start&target_interface_id=finish&vrf=Production", "", "s")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":"pairing"`) || !strings.Contains(w.Body.String(), `"reachable":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := a.db.Exec(`DELETE FROM patch_pairings`); e != nil {
		t.Fatal(e)
	}
	w = apiRequest(a, "GET", "/api/trace?interface_id=start&target_interface_id=finish&vrf=Production", "", "s")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reachable":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestVRFOverlaySnapshotFiltering(t *testing.T) {
	a := vrfFixture(t)
	multiPort(t, a, "prod", "switch", "Production")
	multiPort(t, a, "guest", "switch", "Guest")
	tx, e := a.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	s, e := loadTopology(tx, a.db, 1)
	if e != nil {
		t.Fatal(e)
	}
	if !containsVRF(s.ObjectVRFs["site"], "Production") || !reflect.DeepEqual(s.PortVRFs["guest"], []string{"Guest"}) {
		t.Fatal(s.ObjectVRFs, s.PortVRFs)
	}
	scene, e := s.scene("", "global", "", "", "", "Missing")
	if e != nil {
		t.Fatal(e)
	}
	if len(scene.Nodes) != 0 || len(scene.Edges) != 0 {
		t.Fatal(scene)
	}
	scene, e = s.scene("", "global", "", "", "", "Production")
	if e != nil {
		t.Fatal(e)
	}
	if len(scene.Nodes) != 1 || scene.Nodes[0].ID != "site" || !containsVRF(scene.Nodes[0].VRFs, "Production") {
		t.Fatal(scene)
	}
}
func TestNetworkComparisonMovesAndStableIdentity(t *testing.T) {
	before := reviewSnapshot{Format: "mapper.topology.v1", Entities: []reviewEntity{{ID: "same", Kind: "device", Name: "Router", Data: map[string]any{"rack_id": "a"}}, {ID: "gone", Kind: "device"}}}
	after := reviewSnapshot{Format: before.Format, Entities: []reviewEntity{{ID: "same", Kind: "device", Name: "Router", Data: map[string]any{"rack_id": "b"}}, {ID: "new", Kind: "device", Name: "Router"}}}
	changes, e := compareReviews(before, after)
	if e != nil {
		t.Fatal(e)
	}
	kinds := map[string]int{}
	for _, c := range changes {
		kinds[c.Type]++
	}
	if kinds["added"] != 1 || kinds["removed"] != 1 || kinds["moved"] != 1 {
		t.Fatal(changes)
	}
	if _, e = compareReviews(reviewSnapshot{}, after); e == nil {
		t.Fatal("accepted invalid format")
	}
	after.Entities = append(after.Entities, after.Entities[0])
	if _, e = compareReviews(before, after); e == nil {
		t.Fatal("accepted duplicate identity")
	}
}
func TestNetworkSnapshotsAPIAndPagination(t *testing.T) {
	a := vrfFixture(t)
	w := apiRequest(a, "GET", "/api/topology/snapshot", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var before reviewSnapshot
	if e := json.Unmarshal(w.Body.Bytes(), &before); e != nil {
		t.Fatal(e)
	}
	if len(before.Entities) < 4 {
		t.Fatal(before)
	}
	after := before
	after.Entities = append([]reviewEntity{}, before.Entities...)
	after.Entities[0].X += 10
	payload, _ := json.Marshal(map[string]any{"before": before, "after": after})
	w = apiRequest(a, "POST", "/api/topology/compare", string(payload), "s")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"moved":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, "GET", "/api/topology/compare?before_network=one&after_network=two", "", "s")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiRequest(a, "POST", "/api/topology/snapshot", "{}", "s")
	if w.Code != 405 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, value := range []any{"red", []any{"red", "blue"}, "blue, red"} {
		if !networkIncludes(value, "red") {
			t.Fatal(value)
		}
	}
	ports := []analysisPort{{ID: "a", Networks: []string{"red", "blue"}, VRFs: []string{"prod"}, IP: "192.0.2.1"}, {ID: "b", Networks: []string{"blue"}, VRFs: []string{"prod"}, IP: "192.0.2.1"}}
	issues := qualityIssues(ports)
	if len(issues) != 2 || !strings.Contains(issues[1].Scope, "blue") {
		t.Fatal(issues)
	}
}

func TestSnapshotsIncludeScopedMovesAndCablePlant(t *testing.T) {
	a := vrfFixture(t)
	for _, q := range []string{
		`INSERT INTO layouts(grouping,object_id,x,y) VALUES('scope:floor:ground','router',10,20)`,
		`INSERT INTO relationships(id,source_id,target_id,relationship_type,label) VALUES('dep','router','switch','depends_on','Dependency')`,
	} {
		if _, e := a.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	capture := func() reviewSnapshot {
		t.Helper()
		w := apiRequest(a, "GET", "/api/topology/snapshot", "", "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var snapshot reviewSnapshot
		if e := json.Unmarshal(w.Body.Bytes(), &snapshot); e != nil {
			t.Fatal(e)
		}
		return snapshot
	}
	before := capture()
	if _, e := a.db.Exec(`UPDATE layouts SET x=30 WHERE object_id='router'`); e != nil {
		t.Fatal(e)
	}
	after := capture()
	changes, e := compareReviews(before, after)
	if e != nil {
		t.Fatal(e)
	}
	if len(changes) != 1 || changes[0].Type != "moved" || changes[0].After.Kind != "layout" {
		t.Fatal(changes)
	}
	found := false
	for _, entity := range before.Entities {
		if entity.Kind == "relationship" {
			found = true
		}
	}
	if !found {
		t.Fatal("relationship missing")
	}
}

func TestComparisonPagingAndViewerReadOnly(t *testing.T) {
	a := vrfFixture(t)
	a.sessions["v"] = principal{User: "v", Role: "viewer"}
	a.sessionExpires["v"] = time.Now().Add(time.Hour)
	before := reviewSnapshot{Format: "mapper.topology.v1", Entities: []reviewEntity{}}
	after := reviewSnapshot{Format: before.Format, Entities: []reviewEntity{}}
	for i := 0; i < 205; i++ {
		after.Entities = append(after.Entities, reviewEntity{ID: fmt.Sprintf("d%03d", i), Kind: "device", Data: map[string]any{}})
	}
	body, _ := json.Marshal(map[string]any{"before": before, "after": after})
	seen := map[string]bool{}
	cursor := ""
	for {
		w := apiRequest(a, "POST", "/api/topology/compare?after="+cursor, string(body), "v")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Changes []reviewChange `json:"changes"`
			Next    string         `json:"next_cursor"`
			Total   int            `json:"total"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if len(result.Changes) > 100 || result.Total != 205 {
			t.Fatal(result)
		}
		for _, c := range result.Changes {
			if seen[c.After.ID] {
				t.Fatal("duplicate", c)
			}
			seen[c.After.ID] = true
		}
		if result.Next == "" {
			break
		}
		cursor = result.Next
	}
	if len(seen) != 205 {
		t.Fatal(len(seen))
	}
	for _, path := range []string{"/api/topology/snapshot", "/api/topology/quality"} {
		w := apiRequest(a, "GET", path, "", "v")
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}
func TestQualityMemberPagination(t *testing.T) {
	a := vrfFixture(t)
	tx, e := a.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 205; i++ {
		_, e = tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,'{"ip_address":"192.0.2.1","vrfs":["Production"]}')`, fmt.Sprintf("port%03d", i), "router", fmt.Sprintf("port%03d", i))
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, offset := range []int{0, 100, 200} {
		w := apiRequest(a, "GET", fmt.Sprintf("/api/topology/quality?kind=address_reuse&address=192.0.2.1&port_offset=%d", offset), "", "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Issues []qualityIssue `json:"issues"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if len(result.Issues) != 1 || result.Issues[0].PortCount != 205 {
			t.Fatal(result)
		}
		for _, p := range result.Issues[0].Ports {
			if seen[p.ID] {
				t.Fatal(p)
			}
			seen[p.ID] = true
		}
	}
	if len(seen) != 205 {
		t.Fatal(len(seen))
	}
}

func BenchmarkNetworkQuality100K(b *testing.B) {
	ports := make([]analysisPort, 100000)
	for i := range ports {
		ports[i] = analysisPort{ID: fmt.Sprint(i), Networks: []string{"production"}, VRFs: []string{"Management"}, IP: fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		qualityIssues(ports)
	}
}
func BenchmarkNetworkCompare100K(b *testing.B) {
	before := reviewSnapshot{Format: "mapper.topology.v1", Entities: make([]reviewEntity, 100000)}
	for i := range before.Entities {
		before.Entities[i] = reviewEntity{ID: fmt.Sprint(i), Kind: "device", Data: map[string]any{"network": "production"}}
	}
	after := before
	after.Entities = append([]reviewEntity{}, before.Entities...)
	after.Entities[50000].X = 10
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := compareReviews(before, after); e != nil {
			b.Fatal(e)
		}
	}
}

func TestCurrentNetworkComparisonAndSequenceGuard(t *testing.T) {
	a := vrfFixture(t)
	for _, q := range []string{`UPDATE objects SET data=json_set(data,'$.network',json('["red","shared"]')) WHERE id='router'`, `UPDATE objects SET data=json_set(data,'$.network',json('["blue","shared"]')) WHERE id='switch'`} {
		if _, e := a.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	w := apiRequest(a, "GET", "/api/topology/compare?before_network=red&after_network=blue", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Changes       []reviewChange `json:"changes"`
		Counts        map[string]int `json:"counts"`
		Before, After int64
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	foundRouter, foundSwitch := false, false
	for _, change := range result.Changes {
		if change.Type == "removed" && change.Before.ID == "router" {
			foundRouter = true
		}
		if change.Type == "added" && change.After.ID == "switch" {
			foundSwitch = true
		}
	}
	if !foundRouter || !foundSwitch {
		t.Fatal(w.Body.String())
	}
	w = apiRequest(a, "GET", "/api/topology/compare?before_network=red&after_network=blue&sequence=-1", "", "s")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestVRFOverlayUsesConnectionInterfaces(t *testing.T) {
	a := vrfFixture(t)
	multiPort(t, a, "rprod", "router", "Production")
	multiPort(t, a, "sprod", "switch", "Production")
	multiPort(t, a, "sguest", "switch", "Guest")
	if _, e := a.db.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES('mismatch','rprod','sguest','{}')`); e != nil {
		t.Fatal(e)
	}
	tx, e := a.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	s, e := loadTopology(tx, a.db, 1)
	if e != nil {
		t.Fatal(e)
	}
	floor := s.ancestor("router", "floor")
	if floor == "" {
		t.Fatal("fixture missing floor")
	}
	scene, e := s.scene(floor, "floor", "", "", "", "Production")
	if e != nil {
		t.Fatal(e)
	}
	if len(scene.Nodes) != 2 || len(scene.Edges) != 0 {
		t.Fatalf("mismatched port leaked into VRF view: %+v", scene)
	}
	scene, e = s.scene(floor, "floor", "", "", "", "")
	if e != nil {
		t.Fatal(e)
	}
	if len(scene.Edges) != 1 {
		t.Fatal(scene)
	}
}
