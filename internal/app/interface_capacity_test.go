package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestNetworkRateParsing(t *testing.T) {
	for text, want := range map[string]int64{"1G": 1000000000, "1Gbps": 1000000000, "1000Mbps": 1000000000, " 2.5 Gbps ": 2500000000, "0.001Kbps": 1, "100Gb/s": 100000000000, "123bps": 123} {
		got, err := parseNetworkRate(text)
		if err != nil || got != want {
			t.Fatalf("%s: %d, %v", text, got, err)
		}
	}
	for _, text := range []string{"", "0", "-1G", "auto", "1GiB", "1GBps", "1e9", "0.1bps", "9999999999999999999999G", "NaN", "Inf", strings.Repeat("9", 65)} {
		if _, err := parseNetworkRate(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func capacityPost(a *App, id, owner string, meta map[string]any, revision string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"id": id, "device_id": owner, "name": id, "data": meta, "expected_revision": revision})
	return apiRequest(a, http.MethodPost, "/api/interfaces", string(body), "s")
}
func capacityPort(t *testing.T, a *App, id string) map[string]string {
	t.Helper()
	w := apiRequest(a, http.MethodGet, "/api/interfaces?ids="+id, "", "s")
	var ports []map[string]string
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ports) != nil || len(ports) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	return ports[0]
}
func capacityMeta(t *testing.T, a *App, id string) map[string]any {
	t.Helper()
	port := capacityPort(t, a, id)
	var meta map[string]any
	if err := json.Unmarshal([]byte(port["data"]), &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}
func requireCapacityStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPhysicalInterfaceSpeedsAndOptionalUplinks(t *testing.T) {
	a := vrfFixture(t)
	for _, meta := range []map[string]any{{}, {"speed": ""}, {"speed": "1GiB"}, {"speed": "0Mbps"}, {"speed": "1G", "interface_type": "passive"}, {"speed": "1G", "interface_usage": "unknown"}} {
		requireCapacityStatus(t, capacityPost(a, "bad", "switch", meta, ""), 400)
	}
	requireCapacityStatus(t, capacityPost(a, "regular", "switch", map[string]any{"speed": "1G", "role": "red"}, ""), 204)
	meta := capacityMeta(t, a, "regular")
	if meta["interface_usage"] != "regular" || meta["speed_bps"] != float64(1000000000) || meta["role"] != "red" {
		t.Fatal(meta)
	}
	requireCapacityStatus(t, capacityPost(a, "uplink", "switch", map[string]any{"speed": "1G", "interface_usage": "uplink"}, ""), 204)
	requireCapacityStatus(t, capacityPost(a, "loopback", "switch", map[string]any{"interface_type": "logical"}, ""), 204)
	if _, err := a.db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES('panel','patch_panel','Panel','{}',0,0)`); err != nil {
		t.Fatal(err)
	}
	requireCapacityStatus(t, capacityPost(a, "panel-port", "panel", map[string]any{"interface_usage": "uplink"}, ""), 204)
	// The original automatically created physical port remains incomplete and is never assigned a fabricated speed.
	ports := readVRFPorts(t, a, "router")
	if len(ports[0]["validation_issues"].([]any)) == 0 {
		t.Fatal(ports)
	}
	var raw string
	_ = a.db.QueryRow(`SELECT data FROM interfaces WHERE device_id='router'`).Scan(&raw)
	if strings.Contains(raw, "speed") {
		t.Fatal(raw)
	}
}

func TestRouterUplinkCapacityLimits(t *testing.T) {
	a := vrfFixture(t)
	exact := map[string]any{"speed": "1Gbps", "interface_usage": "uplink", "vrf_mode": "all", "vrf_bandwidth_bps": map[string]any{"Management": 400000000, "Production": 400000000, "Guest": 200000000}}
	requireCapacityStatus(t, capacityPost(a, "wan", "router", exact, ""), 204)
	meta := capacityMeta(t, a, "wan")
	if meta["allocated_bps"] != float64(1000000000) || meta["remaining_bps"] != float64(0) || len(meta["validation_issues"].([]any)) != 0 {
		t.Fatal(meta)
	}
	for name, patch := range map[string]map[string]any{
		"over":                     {"vrf_bandwidth_bps": map[string]any{"Management": 400000000, "Production": 400000000, "Guest": 200000001}},
		"missing":                  {"vrf_bandwidth_bps": map[string]any{"Management": 100000000}},
		"unknown":                  {"vrf_bandwidth_bps": map[string]any{"Unknown": 100000000}},
		"negative":                 {"vrf_bandwidth_bps": map[string]any{"Management": -1}},
		"fraction":                 {"vrf_bandwidth_bps": map[string]any{"Management": 0.5}},
		"overflow":                 {"vrf_bandwidth_bps": map[string]any{"Management": 9007199254740991.0, "Production": 1}},
		"speed reduction":          {"speed": "900Mbps"},
		"logical missing capacity": {"interface_type": "logical", "speed": ""},
	} {
		draft := map[string]any{}
		for k, v := range exact {
			draft[k] = v
		}
		for k, v := range patch {
			draft[k] = v
		}
		if w := capacityPost(a, "wan", "router", draft, ""); w.Code != 400 {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	// One or zero VRFs need no allocation, but supplied allocations must still fit.
	requireCapacityStatus(t, capacityPost(a, "single", "router", map[string]any{"speed": "1G", "interface_usage": "uplink", "vrfs": []string{"Management"}}, ""), 204)
	requireCapacityStatus(t, capacityPost(a, "single", "router", map[string]any{"speed": "1G", "interface_usage": "uplink", "vrfs": []string{"Management"}, "vrf_bandwidth_bps": map[string]any{"Management": 2000000000}}, ""), 400)
	requireCapacityStatus(t, capacityPost(a, "none", "router", map[string]any{"speed": "1G", "interface_usage": "uplink", "vrfs": []string{}}, ""), 204)
	// Changing the selection removes old allocations for no-longer-carried VRFs.
	draft := capacityMeta(t, a, "wan")
	draft["vrf_mode"] = "selected"
	draft["vrfs"] = []string{"Management"}
	requireCapacityStatus(t, capacityPost(a, "wan", "router", draft, ""), 204)
	meta = capacityMeta(t, a, "wan")
	budgets := meta["vrf_bandwidth_bps"].(map[string]any)
	if len(budgets) != 1 || budgets["Management"] != float64(400000000) {
		t.Fatal(meta)
	}
}

func TestUplinkDeviceVRFChangesAndUndo(t *testing.T) {
	a := vrfFixture(t)
	requireCapacityStatus(t, capacityPost(a, "wan", "router", map[string]any{"speed": "1G", "interface_usage": "uplink", "vrf_bandwidth_bps": map[string]any{"Management": 300000000, "Production": 300000000, "Guest": 300000000}}, ""), 204)
	body := `{"id":"router","kind":"device","name":"Router","device_type_id":"router","building_id":"building","vrfs":["Management","Production","Guest","New"]}`
	topologyPost(t, a, body)
	meta := capacityMeta(t, a, "wan")
	if len(meta["validation_issues"].([]any)) != 1 {
		t.Fatal(meta)
	}
	// Explicitly complete the new allocation; imported/derived validation fields are stripped.
	meta["vrf_bandwidth_bps"].(map[string]any)["New"] = float64(100000000)
	requireCapacityStatus(t, capacityPost(a, "wan", "router", meta, ""), 204)
	body = `{"id":"router","kind":"device","name":"Router","device_type_id":"router","building_id":"building","vrfs":["Management","Production","Guest"]}`
	topologyPost(t, a, body)
	meta = capacityMeta(t, a, "wan")
	if _, exists := meta["vrf_bandwidth_bps"].(map[string]any)["New"]; exists {
		t.Fatal(meta)
	}
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/undo", "", "s"), 204)
	meta = capacityMeta(t, a, "wan")
	if meta["vrf_bandwidth_bps"].(map[string]any)["New"] != float64(100000000) {
		t.Fatal(meta)
	}
	// A later port edit prevents an owner undo from restoring stale allocations.
	topologyPost(t, a, body)
	meta = capacityMeta(t, a, "wan")
	meta["description"] = "another user's edit"
	requireCapacityStatus(t, capacityPost(a, "wan", "router", meta, ""), 204)
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/undo", "", "s"), 409)
}

func TestInterfaceRevisionAndIncompleteCSV(t *testing.T) {
	a := vrfFixture(t)
	requireCapacityStatus(t, capacityPost(a, "wan", "router", map[string]any{"speed": "1G"}, ""), 204)
	first := capacityPort(t, a, "wan")
	requireCapacityStatus(t, capacityPost(a, "wan", "router", map[string]any{"speed": "10G"}, first["revision"]), 204)
	requireCapacityStatus(t, capacityPost(a, "wan", "router", map[string]any{"speed": "100G"}, first["revision"]), 409)
	latest := capacityPort(t, a, "wan")
	topologyPost(t, a, `{"id":"router","kind":"device","name":"Renamed router","device_type_id":"router","building_id":"building","vrfs":["Management","Production","Guest","New"]}`)
	requireCapacityStatus(t, capacityPost(a, "wan", "router", map[string]any{"speed": "1G"}, latest["revision"]), 409)
	csvBody := func(id, raw string) string {
		var b strings.Builder
		w := csv.NewWriter(&b)
		_ = w.Write([]string{"kind", "id", "device_id", "name", "metadata_json"})
		_ = w.Write([]string{"interface", id, "router", id, raw})
		w.Flush()
		return b.String()
	}
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		requireCapacityStatus(t, apiRequest(a, http.MethodPost, path, csvBody("imported", `{"interface_usage":"uplink"}`), "s"), 200)
	}
	meta := capacityMeta(t, a, "imported")
	if len(meta["validation_issues"].([]any)) != 5 {
		t.Fatal(meta)
	}
	bad := `{"speed":"1G","interface_usage":"uplink","vrf_bandwidth_bps":{"Management":1000000001}}`
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/import", csvBody("bad", bad), "s"), 400)
	out := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	b, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	requireCapacityStatus(t, apiRequest(b, http.MethodPost, "/api/import", out.Body.String(), "s"), 200)
	if !reflect.DeepEqual(capacityMeta(t, b, "imported")["validation_issues"], meta["validation_issues"]) {
		t.Fatal("incomplete import lost issues")
	}
	var raw string
	_ = a.db.QueryRow(`SELECT data FROM interfaces WHERE id='wan'`).Scan(&raw)
	for _, key := range []string{"validation_issues", "effective_vrfs", "allocated_bps", "remaining_bps"} {
		if strings.Contains(raw, key) {
			t.Fatal(fmt.Sprintf("persisted %s", key), raw)
		}
	}
	page := topologyPage(t, a, "building", "building")
	found := false
	for _, node := range page.Nodes {
		if node.ID == "router" && node.Warning != "" {
			found = true
		}
	}
	// Building scenes collapse equipment into floors. Verify the visible device warning in its floor.
	if !found {
		page = topologyPage(t, a, "floor", "ground:building")
		for _, node := range page.Nodes {
			if node.ID == "router" && node.Warning != "" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("device warning missing")
	}
}

func TestCapacityExactJSONAndLegacyMetadata(t *testing.T) {
	a := vrfFixture(t)
	raw := `{"id":"fraction","device_id":"router","name":"fraction","data":{"speed":"9P","interface_usage":"uplink","vrfs":["Guest"],"vrf_bandwidth_bps":{"Guest":9007199254740990.5}}}`
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/interfaces", raw, "s"), 400)
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/interfaces", strings.Replace(raw, "9007199254740990.5", "1e999999999999999999", 1), "s"), 400)
	var csvBody strings.Builder
	writer := csv.NewWriter(&csvBody)
	_ = writer.Write([]string{"kind", "id", "device_id", "name", "metadata_json"})
	_ = writer.Write([]string{"interface", "fraction", "router", "fraction", `{"speed":"9P","interface_usage":"uplink","vrfs":["Guest"],"vrf_bandwidth_bps":{"Guest":9007199254740990.5}}`})
	writer.Flush()
	requireCapacityStatus(t, apiRequest(a, http.MethodPost, "/api/import", csvBody.String(), "s"), 400)
	for i, raw := range []string{`null`, `[]`, `{"speed":"auto"}`} {
		id := fmt.Sprintf("legacy-%d", i)
		if _, err := a.db.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?)`, id, "switch", id, raw); err != nil {
			t.Fatal(err)
		}
		meta := capacityMeta(t, a, id)
		if len(meta["validation_issues"].([]any)) == 0 {
			t.Fatal(meta)
		}
	}
	topologyPost(t, a, `{"id":"switch","kind":"device","name":"Switch renamed","device_type_id":"layer3-switch","building_id":"building","vrfs":["Management","Production","Guest"]}`)
}
