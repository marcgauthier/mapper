package main

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
)

func TestCSVExportImportRoundTrip(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	inventory := `{"id":"cloud-shape","kind":"shape","name":"Shared, cloud","shape_type":"circle","width":220,"fill_color":"#abcdef","custom":["one","two"],"x":10,"y":20}`
	if w := apiRequest(a, http.MethodPost, "/api/objects", inventory, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var point string
	_ = db.QueryRow(`SELECT id FROM interfaces WHERE device_id='cloud-shape'`).Scan(&point)
	for _, id := range []string{"a", "b"} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,'device',?,'{"device_type_id":"nexus-7000","device_type":"Cisco Nexus 7000","building_id":"B1"}',0,0)`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{"ip_address":"192.0.2.1/24","custom":[1,2]}')`, id+"p", id, "port"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO links VALUES(?,?,?,'{"connection_class":"physical","physical_type":"backplane","label":"Cloud uplink"}')`, id+"l", id+"p", point); err != nil {
			t.Fatal(err)
		}
	}
	w := apiRequest(a, http.MethodGet, "/api/export", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "mapper-topology.csv") {
		t.Fatal(w.Header())
	}
	if _, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll(); err != nil {
		t.Fatal(err)
	}
	target, targetDB := editorApp(t, "editor")
	if err := migrateSchema(targetDB); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		result := apiRequest(target, http.MethodPost, path, w.Body.String(), "s")
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	for _, table := range []string{"objects", "interfaces", "links"} {
		var sourceCount, targetCount int
		_ = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&sourceCount)
		_ = targetDB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&targetCount)
		if sourceCount != targetCount {
			t.Fatal(table, sourceCount, targetCount)
		}
	}
	var data string
	if err := targetDB.QueryRow(`SELECT data FROM objects WHERE id='cloud-shape'`).Scan(&data); err != nil || !strings.Contains(data, `"custom":["one","two"]`) {
		t.Fatal(data, err)
	}
	if err := targetDB.QueryRow(`SELECT data FROM interfaces WHERE id='ap'`).Scan(&data); err != nil || !strings.Contains(data, `"custom":[1,2]`) {
		t.Fatal(data, err)
	}
}

func TestCSVRejectsSecondShapePointAndDevicePeer(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	data := "kind,id,name,device_id,source_interface,target_interface,connection_class,virtual_type\n" + "shape,s,Box,,,,,\ninterface,p,Point,s,,,,\ninterface,p2,Other,s,,,,\n"
	if w := apiRequest(a, http.MethodPost, "/api/import", data, "s"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	data = "kind,id,name,device_id,source_interface,target_interface,connection_class,virtual_type\n" + "device,a,A,,,,,\ndevice,b,B,,,,,\ndevice,c,C,,,,,\ninterface,ap,port,a,,,,\ninterface,bp,port,b,,,,\ninterface,cp,port,c,,,,\nconnection,l1,,,ap,bp,virtual,ethernet\nconnection,l2,,,cp,bp,virtual,ethernet\n"
	if w := apiRequest(a, http.MethodPost, "/api/import?dry_run=1", data, "s"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM objects`).Scan(&count)
	if count != 0 {
		t.Fatal("failed import did not roll back", count)
	}
}
