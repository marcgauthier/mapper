package main

import (
	"net/http"
	"testing"
)

func TestPlacementRequiresNamedSquare(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('s1','site','HQ','{}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('b1','building','B1','{"site_id":"s1"}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('r1','telecom_room','MDF','{"site_id":"s1","building_id":"b1"}',0,0)`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('rk1','rack','R1','{"site_id":"s1","building_id":"b1","telecom_room_id":"r1"}',0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	post := func(body string) int {
		return apiRequest(a, http.MethodPost, "/api/objects", body, "s").Code
	}
	for name, body := range map[string]string{
		"device placed":  `{"id":"d1","kind":"device","name":"sw","building_id":"b1"}`,
		"panel in rack":  `{"id":"p1","kind":"patch_panel","name":"pp","rack_id":"rk1"}`,
		"room device":    `{"id":"d2","kind":"device","name":"sw","telecom_room_id":"r1"}`,
		"free text":      `{"id":"d3","kind":"device","name":"sw","building_id":"Not created yet"}`,
		"building":       `{"id":"b9","kind":"building","name":"B9","site_id":"s1"}`,
		"room":           `{"id":"r9","kind":"telecom_room","name":"IDF","building_id":"b1"}`,
		"rack in room":   `{"id":"rk9","kind":"rack","name":"R9","telecom_room_id":"r1"}`,
		"rack in bldg":   `{"id":"rk8","kind":"rack","name":"R8","building_id":"b1"}`,
		"site exempt":    `{"id":"s9","kind":"site","name":"Branch"}`,
		"manhole exempt": `{"id":"m9","kind":"manhole","name":"MH9"}`,
		"label exempt":   `{"id":"l9","kind":"label","name":"note"}`,
	} {
		if code := post(body); code != 204 {
			t.Fatalf("%s: status=%d", name, code)
		}
	}
	for name, body := range map[string]string{
		"bare device":   `{"id":"x1","kind":"device","name":"sw"}`,
		"site-only":     `{"id":"x2","kind":"device","name":"sw","site_id":"s1"}`,
		"blank room":    `{"id":"x3","kind":"device","name":"sw","telecom_room_id":"  "}`,
		"bare panel":    `{"id":"x4","kind":"patch_panel","name":"pp"}`,
		"bare building": `{"id":"x5","kind":"building","name":"B"}`,
		"bare room":     `{"id":"x6","kind":"telecom_room","name":"R"}`,
		"bare rack":     `{"id":"x7","kind":"rack","name":"R"}`,
		"site-only rack": `{"id":"x8","kind":"rack","name":"R","site_id":"s1"}`,
	} {
		if code := post(body); code != 400 {
			t.Fatalf("%s: status=%d want 400", name, code)
		}
	}
	if w := apiRequest(a, http.MethodPost, "/api/import", "kind,id,name,building_id\ndevice,imp1,sw,\n", "s"); w.Code != 400 {
		t.Fatalf("import bare device: status=%d want 400", w.Code)
	}
}
