package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRackPlacementAndLocationHierarchy(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"site","kind":"site","name":"Campus","site_role":"major_site"}`,
		`{"id":"building","kind":"building","name":"Building A","site_id":"site"}`,
		`{"id":"room","kind":"telecom_room","name":"MDF","building_id":"building"}`,
		`{"id":"rack","kind":"rack","name":"Rack 01","telecom_room_id":"room"}`,
		`{"id":"host","kind":"device","name":"Proxmox","device_type_id":"proxmox","rack_id":"rack"}`,
		`{"id":"vm","kind":"virtual_machine","name":"VM","device_type_id":"vm","host_id":"host","guest_os":"Linux"}`,
		`{"id":"vs","kind":"virtual_switch","name":"vSwitch","device_type_id":"vswitch","host_id":"host"}`,
	} {
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"rack", "host", "vm", "vs"} {
		var raw string
		if err := db.QueryRow(`SELECT data FROM objects WHERE id=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		_ = json.Unmarshal([]byte(raw), &meta)
		if meta["site_id"] != "site" || meta["building_id"] != "building" || meta["telecom_room_id"] != "room" {
			t.Fatal(id, meta)
		}
		if id != "rack" && meta["rack_id"] != "rack" {
			t.Fatal(id, meta)
		}
	}
	for _, id := range []string{"site", "building", "room", "rack"} {
		if w := apiRequest(a, http.MethodDelete, "/api/objects?id="+id, "", "s"); w.Code != 409 {
			t.Fatal(id, w.Code, w.Body.String())
		}
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"kind":"device","name":"bad","rack_id":"unknown"}`, "s"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"rack","kind":"shape","name":"Converted rack"}`, "s"); w.Code != 409 {
		t.Fatal("converted a rack with devices", w.Code, w.Body.String())
	}
	w := apiRequest(a, http.MethodGet, "/api/objects?container_ids=rack", "", "s")
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 3 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	export := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	target, targetDB := editorApp(t, "editor")
	if err := migrateSchema(targetDB); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(target, http.MethodPost, "/api/import", export.Body.String(), "s"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	_ = targetDB.QueryRow(`SELECT count(*) FROM objects WHERE json_extract(data,'$.rack_id')='rack'`).Scan(&count)
	if count != 3 {
		t.Fatal(count)
	}
}
