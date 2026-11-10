package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestHypervisorPlatformsHostVMsAndVSwitches(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"hypervisor", "esxi", "vmware", "hyper-v", "proxmox", "kvm", "xen", "xcp-ng", "nutanix-ahv", "ovirt", "bhyve"} {
		t.Run(platform, func(t *testing.T) {
			host := "host-" + platform
			body := fmt.Sprintf(`{"id":%q,"kind":"device","building_id":"B1","name":%q,"device_type_id":%q,"site_id":"site-1","network":["mgmt"]}`, host, host, platform)
			if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, kind := range []string{"virtual_machine", "virtual_switch"} {
				typeID := "vm"
				if kind == "virtual_switch" {
					typeID = "vswitch"
				}
				id := host + "-" + kind
				body = fmt.Sprintf(`{"id":%q,"kind":%q,"name":%q,"device_type_id":%q,"host_id":%q,"guest_os":"linux"}`, id, kind, id, typeID, host)
				if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
					t.Fatal(w.Code, w.Body.String())
				}
				var raw string
				if err := db.QueryRow(`SELECT data FROM objects WHERE id=?`, id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var meta map[string]any
				_ = json.Unmarshal([]byte(raw), &meta)
				if meta["site_id"] != "site-1" || meta["host_id"] != host || len(meta["network"].([]any)) != 1 {
					t.Fatal(meta)
				}
				if kind == "virtual_machine" && meta["guest_os"] != "Linux" {
					t.Fatal(meta)
				}
				var ports int
				if err := db.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=?`, id).Scan(&ports); err != nil || ports != 1 {
					t.Fatal(ports, err)
				}
			}
			w := apiRequest(a, http.MethodGet, "/api/objects?host_ids="+host, "", "s")
			var children []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &children); err != nil || len(children) != 2 {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			if w := apiRequest(a, http.MethodDelete, "/api/objects?id="+host, "", "s"); w.Code != 409 {
				t.Fatal("deleted occupied hypervisor", w.Code, w.Body.String())
			}
			body = fmt.Sprintf(`{"id":%q,"kind":"device","building_id":"B1","name":"ordinary","device_type_id":"server"}`, host)
			if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 409 {
				t.Fatal("converted occupied hypervisor", w.Code, w.Body.String())
			}
		})
	}
	w := apiRequest(a, http.MethodGet, "/api/device-types", "", "s")
	var catalog []deviceType
	_ = json.Unmarshal(w.Body.Bytes(), &catalog)
	for _, item := range catalog {
		if item.ID == "proxmox" && !item.CanHostVMs {
			t.Fatal("missing catalog hypervisor capability")
		}
	}
}

func TestHostedItemsRejectOrdinaryAndMissingParents(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"ordinary","kind":"device","building_id":"B1","name":"server","device_type_id":"server"}`, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, kind := range []string{"virtual_machine", "virtual_switch"} {
		for _, host := range []string{"ordinary", "missing", ""} {
			typeID := "vm"
			if kind == "virtual_switch" {
				typeID = "vswitch"
			}
			body := fmt.Sprintf(`{"kind":%q,"name":"child","device_type_id":%q,"host_id":%q,"guest_os":"Linux"}`, kind, typeID, host)
			if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestVSwitchPortsConnectSeparateVMs(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"host","kind":"device","building_id":"B1","name":"Proxmox","device_type_id":"proxmox"}`,
		`{"id":"switch","kind":"virtual_switch","name":"vSwitch0","device_type_id":"vswitch","host_id":"host"}`,
		`{"id":"vm1","kind":"virtual_machine","name":"VM1","device_type_id":"vm","host_id":"host","guest_os":"Linux"}`,
		`{"id":"vm2","kind":"virtual_machine","name":"VM2","device_type_id":"vm","host_id":"host","guest_os":"Windows"}`,
	} {
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var port1, vm1, vm2 string
	_ = db.QueryRow(`SELECT id FROM interfaces WHERE device_id='switch'`).Scan(&port1)
	_ = db.QueryRow(`SELECT id FROM interfaces WHERE device_id='vm1'`).Scan(&vm1)
	_ = db.QueryRow(`SELECT id FROM interfaces WHERE device_id='vm2'`).Scan(&vm2)
	if w := apiRequest(a, http.MethodPost, "/api/interfaces", `{"id":"port2","device_id":"switch","name":"Port 2","data":{}}`, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, pair := range [][2]string{{vm1, port1}, {vm2, port1}, {vm2, "port2"}} {
		body := fmt.Sprintf(`{"source_interface":%q,"target_interface":%q,"data":{"connection_class":"virtual","virtual_type":"ethernet"}}`, pair[0], pair[1])
		w := apiRequest(a, http.MethodPost, "/api/links", body, "s")
		want := 204
		if i == 1 {
			want = 409
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := apiRequest(a, http.MethodGet, "/api/links?device_ids=switch", "", "s")
	var links []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &links); err != nil || len(links) != 2 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	exported := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	target, targetDB := editorApp(t, "editor")
	if err := migrateSchema(targetDB); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(target, http.MethodPost, "/api/import", exported.Body.String(), "s"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var children int
	_ = targetDB.QueryRow(`SELECT count(*) FROM objects WHERE json_extract(data,'$.host_id')='host'`).Scan(&children)
	if children != 3 {
		t.Fatal(children)
	}
}

func TestCatalogCannotRemoveOccupiedHypervisorCapability(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, request := range [][3]string{
		{http.MethodPost, "/api/device-types", `{"id":"platform","name":"Custom platform","object_kind":"device","visual":"hypervisor"}`},
		{http.MethodPost, "/api/objects", `{"id":"host","kind":"device","building_id":"B1","name":"Host","device_type_id":"platform"}`},
		{http.MethodPost, "/api/objects", `{"id":"vs","kind":"virtual_switch","name":"Switch","device_type_id":"vswitch","host_id":"host"}`},
	} {
		if w := apiRequest(a, request[0], request[1], request[2], "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := apiRequest(a, http.MethodPut, "/api/device-types", `{"id":"platform","name":"Ordinary server","object_kind":"device","visual":"server"}`, "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodGet, "/api/device-types", "", "s"); !strings.Contains(w.Body.String(), `"virtual_switch"`) {
		t.Fatal(w.Body.String())
	}
}
