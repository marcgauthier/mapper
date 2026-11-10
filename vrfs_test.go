package main

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func vrfFixture(t *testing.T) *App {
	t.Helper()
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"site","kind":"site","name":"Campus"}`,
		`{"id":"building","kind":"building","name":"Building","site_id":"site"}`,
		`{"id":"router","kind":"device","name":"Router","device_type_id":"router","building_id":"building","vrfs":["Management","Production","Guest"]}`,
		`{"id":"switch","kind":"device","name":"Switch","device_type_id":"layer3-switch","building_id":"building","vrfs":["Management","Production","Guest"]}`,
	} {
		topologyPost(t, a, body)
	}
	return a
}

func readVRFPorts(t *testing.T, a *App, owner string) []map[string]any {
	t.Helper()
	w := apiRequest(a, http.MethodGet, "/api/interfaces?device_id="+owner, "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var rows []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, row := range rows {
		var meta map[string]any
		if err := json.Unmarshal([]byte(row["data"]), &meta); err != nil {
			t.Fatal(err)
		}
		meta["id"] = row["id"]
		out = append(out, meta)
	}
	return out
}

func TestRouterInterfaceVRFDefaultsAndSubsets(t *testing.T) {
	a := vrfFixture(t)
	ports := readVRFPorts(t, a, "router")
	if len(ports) != 1 || ports[0]["vrf_mode"] != "all" {
		t.Fatal(ports)
	}
	expected := []any{"Management", "Production", "Guest"}
	if !reflect.DeepEqual(ports[0]["effective_vrfs"], expected) {
		t.Fatal(ports)
	}
	for _, body := range []string{
		`{"id":"all-port","device_id":"router","name":"All port","data":{"custom":"preserved","speed":"1G"}}`,
		`{"id":"subset-port","device_id":"router","name":"Subset port","data":{"vrfs":["Management","Guest"],"speed":"1G"}}`,
		`{"id":"none-port","device_id":"router","name":"No VRFs","data":{"vrf_mode":"selected","vrfs":[],"speed":"1G"}}`,
	} {
		w := apiRequest(a, http.MethodPost, "/api/interfaces", body, "s")
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	topologyPost(t, a, `{"id":"router","kind":"device","name":"Router","device_type_id":"router","building_id":"building","vrfs":["Management","Production","Guest","New VRF"]}`)
	for _, port := range readVRFPorts(t, a, "router") {
		switch port["id"] {
		case "all-port":
			if port["vrf_mode"] != "all" || !reflect.DeepEqual(port["effective_vrfs"], []any{"Management", "Production", "Guest", "New VRF"}) || port["custom"] != "preserved" {
				t.Fatal(port)
			}
		case "subset-port":
			if port["vrf_mode"] != "selected" || !reflect.DeepEqual(port["effective_vrfs"], []any{"Management", "Guest"}) {
				t.Fatal(port)
			}
		case "none-port":
			if len(port["effective_vrfs"].([]any)) != 0 {
				t.Fatal(port)
			}
		}
	}
	// Derived read values must never be persisted or exported as definitions.
	var raw string
	_ = a.db.QueryRow(`SELECT data FROM interfaces WHERE id='all-port'`).Scan(&raw)
	if strings.Contains(raw, "effective_vrfs") || !strings.Contains(raw, `"vrf_mode":"all"`) {
		t.Fatal(raw)
	}
}

func TestSwitchInterfaceVRFValidationAndDeviceEdits(t *testing.T) {
	a := vrfFixture(t)
	w := apiRequest(a, http.MethodPost, "/api/interfaces", `{"id":"switch-port","device_id":"switch","name":"Gi1/0/2","data":{"vrfs":["Management"],"description":"uplink","speed":"1G"}}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{
		`{"device_id":"switch","name":"bad","data":{"vrfs":["Management","Guest"],"speed":"1G"}}`,
		`{"device_id":"switch","name":"bad","data":{"vrf_mode":"all"}}`,
		`{"device_id":"switch","name":"bad","data":{"vrfs":["Unknown"]}}`,
		`{"device_id":"router","name":"bad","data":{"vrfs":["Unknown"]}}`,
		`{"device_id":"router","name":"bad","data":{"vrfs":"Guest"}}`,
		`{"device_id":"router","name":"bad","data":{"vrf_mode":"invalid"}}`,
	} {
		w = apiRequest(a, http.MethodPost, "/api/interfaces", body, "s")
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{
		`{"id":"switch","kind":"device","name":"Switch","device_type_id":"switch","building_id":"building","vrfs":["Guest"]}`,
		`{"id":"switch","kind":"device","name":"Switch","device_type_id":"server","building_id":"building","vrfs":[]}`,
		`{"id":"switch","kind":"device","name":"Switch","device_type_id":"switch","building_id":"building","vrfs":["Management","Management"]}`,
	} {
		w = apiRequest(a, http.MethodPost, "/api/objects", body, "s")
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	// Clear the interface first; removing the definition is then safe.
	w = apiRequest(a, http.MethodPost, "/api/interfaces", `{"id":"switch-port","device_id":"switch","name":"Gi1/0/2","data":{"vrfs":[],"speed":"1G"}}`, "s")
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	topologyPost(t, a, `{"id":"switch","kind":"device","name":"Switch","device_type_id":"switch","building_id":"building","vrfs":["Guest"]}`)
}

func TestVRFCSVAndCatalogValidation(t *testing.T) {
	a := vrfFixture(t)
	csvBody := func(raw string) string {
		var s strings.Builder
		w := csv.NewWriter(&s)
		_ = w.Write([]string{"kind", "id", "device_id", "name", "metadata_json"})
		_ = w.Write([]string{"interface", "csv-port", "router", "CSV port", raw})
		w.Flush()
		return s.String()
	}
	invalid := csvBody(`{"vrfs":["Missing"]}`)
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		w := apiRequest(a, http.MethodPost, path, invalid, "s")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := apiRequest(a, http.MethodPost, "/api/import", csvBody(`{"vrf_mode":"all"}`), "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A catalog style change must not invalidate device or interface VRFs.
	w = apiRequest(a, http.MethodPut, "/api/device-types", `{"id":"router","name":"Router","object_kind":"device","visual":"server","sort_order":100}`, "s")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	export := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	if export.Code != 200 {
		t.Fatal(export.Code, export.Body.String())
	}
	b, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	w = apiRequest(b, http.MethodPost, "/api/import", export.Body.String(), "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ports := readVRFPorts(t, b, "router")
	for _, port := range ports {
		if port["vrf_mode"] != "all" || !reflect.DeepEqual(port["effective_vrfs"], []any{"Management", "Production", "Guest"}) {
			t.Fatal(port)
		}
	}
}
