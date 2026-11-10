package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestEditInterfaceOn96PortDevicePreservesConnections(t *testing.T) {
	a, db := editorApp(t, "editor")
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('switch','device','96 port switch','{}',0,0),('peer','device','peer','{}',0,0)`,
		`INSERT INTO interfaces VALUES('peer1','peer','eth0','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 96; i++ {
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,?)`, fmt.Sprint("p", i), "switch", fmt.Sprint("Ethernet", i), `{"custom":"preserved"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO links VALUES('c','p48','peer1','{"physical_type":"backplane","connection_class":"physical"}')`); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"p48","device_id":"switch","name":"uplink","data":{"custom":"preserved","ip_address":"192.0.2.1/24","connector":"SFP+","speed":"10G","description":"Updated uplink"}}`
	if w := apiRequest(a, http.MethodPost, "/api/interfaces", body, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := apiRequest(a, http.MethodGet, "/api/interfaces?device_id=switch&limit=5000", "", "s")
	var rows []struct{ ID, Name, Data string }
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 96 {
		t.Fatal(len(rows), err, w.Body.String())
	}
	found := false
	for _, row := range rows {
		if row.ID == "p48" {
			found = true
			if row.Name != "uplink" {
				t.Fatal(row)
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(row.Data), &meta); err != nil || meta["custom"] != "preserved" || meta["speed"] != "10G" {
				t.Fatal(meta, err)
			}
		}
	}
	if !found {
		t.Fatal("edited interface lost")
	}
	var endpoint string
	if err := db.QueryRow(`SELECT source_interface FROM links WHERE id='c'`).Scan(&endpoint); err != nil || endpoint != "p48" {
		t.Fatal(endpoint, err)
	}
	duplicate := `{"id":"p48","device_id":"switch","name":"Ethernet49","data":{}}`
	if w := apiRequest(a, http.MethodPost, "/api/interfaces", duplicate, "s"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/interfaces?id=p48", "", "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
