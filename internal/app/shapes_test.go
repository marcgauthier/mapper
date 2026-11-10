package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestShapeAppearanceAndSharedPoint(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"square", "rectangle", "circle", "cloud"} {
		body := fmt.Sprintf(`{"id":%q,"kind":"shape","name":%q,"shape_type":%q,"width":240,"height":120,"fill_color":"#123456"}`, kind, kind, kind)
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
		var data string
		if err := db.QueryRow(`SELECT data FROM objects WHERE id=?`, kind).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		_ = json.Unmarshal([]byte(data), &meta)
		want := float64(240)
		if kind == "rectangle" || kind == "cloud" {
			want = 120
		}
		if meta["height"] != want || meta["width"] != float64(240) || meta["fill_color"] != "#123456" {
			t.Fatal(meta)
		}
	}
	var point string
	if err := db.QueryRow(`SELECT id FROM interfaces WHERE device_id='rectangle'`).Scan(&point); err != nil {
		t.Fatal(err)
	}
	if w := apiRequest(a, http.MethodPost, "/api/interfaces", `{"device_id":"rectangle","name":"extra"}`, "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/interfaces?id="+point, "", "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprint("d", i)
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,'device',?,'{}',0,0)`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{}')`, id+"p", id, "eth0"); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"id":"link%d","source_interface":%q,"target_interface":%q,"data":{"physical_type":"backplane"}}`, i, id+"p", point)
		if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	body := `{"source_interface":"d1p","target_interface":"d2p","data":{"connection_class":"virtual","virtual_type":"ethernet"}}`
	if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 409 {
		t.Fatal("device interface allowed a second peer", w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodGet, "/api/trace?interface_id="+point, "", "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodGet, "/api/trace?interface_id="+point+"&link_id=link1", "", "s"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodDelete, "/api/objects?id=rectangle", "", "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestDeviceInterfaceAllowsOnePeerAcrossConnectionClasses(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := db.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,'device',?,'{}',0,0)`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO interfaces VALUES(?,?,?,'{}')`, id+"p", id, "port"); err != nil {
			t.Fatal(err)
		}
	}
	for i, cls := range []string{"virtual", "logical_tunnel", "provider_service"} {
		spec := map[string]string{"virtual": `"virtual_type":"ethernet"`, "logical_tunnel": `"tunnel_protocol":"gre"`, "provider_service": `"service_type":"mpls"`}[cls]
		body := fmt.Sprintf(`{"id":"l%d","source_interface":"ap","target_interface":"bp","data":{"connection_class":%q,%s}}`, i, cls, spec)
		if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, ends := range [][2]string{{"ap", "cp"}, {"cp", "bp"}} {
			body = fmt.Sprintf(`{"source_interface":%q,"target_interface":%q,"data":{"connection_class":%q,%s}}`, ends[0], ends[1], cls, spec)
			if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestShapeValidationAndUndo(t *testing.T) {
	a, _ := editorApp(t, "editor")
	for _, field := range []string{`"width":0`, `"height":2001`, `"width":"NaN"`, `"fill_color":"red"`, `"shape_type":"triangle"`} {
		body := `{"kind":"shape","name":"Invalid",` + field + `}`
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"shape","kind":"shape","name":"Box"}`, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/undo", "", "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
}
