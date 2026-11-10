package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLabelsPersistAndEditAppearance(t *testing.T) {
	a, db := editorApp(t, "editor")
	create := `{"id":"label1","kind":"label","name":"Production\nNetwork","x":100,"y":150,"font_family":"Georgia","font_size":36,"font_weight":"bold","font_style":"italic","font_color":"#336699"}`
	if w := apiRequest(a, http.MethodPost, "/api/objects", create, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id='label1'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("label should not create interfaces", count, err)
	}
	edit := `{"id":"label1","kind":"label","name":"Updated label","x":100,"y":150,"version":1,"font_family":"Courier New","font_size":20,"font_color":"#112233"}`
	if w := apiRequest(a, http.MethodPost, "/api/objects", edit, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", edit, "s"); w.Code != 409 {
		t.Fatal("stale label edit", w.Code, w.Body.String())
	}
	w := apiRequest(a, http.MethodGet, "/api/objects?kind=label", "", "s")
	var rows []struct {
		Name    string
		Version int
		Data    map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if rows[0].Name != "Updated label" || rows[0].Data["font_family"] != "Courier New" || rows[0].Data["font_size"] != float64(20) || rows[0].Version != 2 {
		t.Fatal(rows)
	}
	if w := apiRequest(a, http.MethodDelete, "/api/objects?id=label1", "", "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLabelValidationAndImport(t *testing.T) {
	a, _ := editorApp(t, "editor")
	for _, field := range []string{`"font_size":0`, `"font_size":145`, `"font_size":12.5`, `"font_family":"unknown"`, `"font_color":"red"`, `"font_color":"#GGGGGG"`, `"font_style":"oblique"`, `"font_weight":"invalid"`} {
		body := `{"kind":"label","name":"Text",` + field + `}`
		if w := apiRequest(a, http.MethodPost, "/api/objects", body, "s"); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	csv := "kind,id,name,font_family,font_size,font_color,font_weight,font_style\nlabel,csvlabel,Imported,Arial,28,#123456,bold,italic\n"
	if w := apiRequest(a, http.MethodPost, "/api/import", csv, "s"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := apiRequest(a, http.MethodGet, "/api/objects?kind=label", "", "s")
	if !strings.Contains(w.Body.String(), `"font_size":28`) {
		t.Fatal(w.Body.String())
	}
}
