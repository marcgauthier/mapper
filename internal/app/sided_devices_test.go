package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSidedDeviceCatalogAndInterfaces(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	seeds := map[string]string{
		"inline-encryptor": "red-black",
		"serial-encryptor": "red-black",
		"airgap":           "low-high",
		"media-converter":  "bridge",
		"container":        "container",
		"pod":              "pod",
		"docker-host":      "server",
		"podman-host":      "server",
		"kubernetes-node":  "server",
	}
	for id, visual := range seeds {
		var got string
		if err := db.QueryRow(`SELECT visual FROM device_types WHERE id=? AND enabled=1`, id).Scan(&got); err != nil || got != visual {
			t.Fatalf("seed %s visual=%q err=%v", id, got, err)
		}
	}
	interfacesOf := func(device string) map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT name,data FROM interfaces WHERE device_id=?`, device)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, raw string
			if err := rows.Scan(&name, &raw); err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			_ = json.Unmarshal([]byte(raw), &decoded)
			role, _ := decoded["role"].(string)
			out[name] = role
		}
		return out
	}
	create := func(id, typeID string) {
		t.Helper()
		body := `{"id":"` + id + `","kind":"device","building_id":"B1","name":"` + id + `","device_type_id":"` + typeID + `","x":0,"y":0}`
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
			t.Fatalf("create %s status=%d %s", id, w.Code, w.Body.String())
		}
	}
	create("enc1", "inline-encryptor")
	if got := interfacesOf("enc1"); len(got) != 2 || got["red"] != "red" || got["black"] != "black" {
		t.Fatalf("inline interfaces=%v", got)
	}
	create("enc2", "serial-encryptor")
	if got := interfacesOf("enc2"); len(got) != 2 || got["red"] != "red" || got["black"] != "black" {
		t.Fatalf("serial interfaces=%v", got)
	}
	create("gap1", "airgap")
	if got := interfacesOf("gap1"); len(got) != 2 || got["low"] != "low" || got["high"] != "high" {
		t.Fatalf("airgap interfaces=%v", got)
	}
	create("sw9", "switch")
	if got := interfacesOf("sw9"); len(got) != 1 || got["Interface 1"] != "" {
		t.Fatalf("plain device interfaces=%v", got)
	}
}

func TestCloudCatalogSeeds(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	seeds := map[string]string{
		"cloud-provider": "cloud",
		"azure":          "cloud",
		"aws":            "cloud",
		"google-cloud":   "cloud",
		"oracle-cloud":   "cloud",
		"saas":           "saas",
		"paas":           "paas",
		"iaas":           "iaas",
		"faas":           "faas",
	}
	for id, visual := range seeds {
		var kind, got string
		if err := db.QueryRow(`SELECT object_kind,visual FROM device_types WHERE id=? AND enabled=1`, id).Scan(&kind, &got); err != nil || kind != "device" || got != visual {
			t.Fatalf("seed %s kind=%q visual=%q err=%v", id, kind, got, err)
		}
	}
	body := `{"id":"crm","kind":"device","building_id":"B1","name":"CRM","device_type_id":"saas","x":0,"y":0}`
	if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
		t.Fatalf("create saas status=%d %s", w.Code, w.Body.String())
	}
	rows, err := db.Query(`SELECT name FROM interfaces WHERE device_id='crm'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 1 || names[0] != "Interface 1" {
		t.Fatalf("saas interfaces=%v", names)
	}
}
