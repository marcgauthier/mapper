package app

import (
	"net/http"
	"os"
	"regexp"
	"testing"
)

// Locations are attributes on items, not addable map item types. The item
// editor must not offer them; they are managed through the location managers,
// which still create them through the objects API.
func TestItemPickerExcludesLocations(t *testing.T) {
	page, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	selectRe := regexp.MustCompile(`(?s)<select id="objectKind">(.*?)</select>`)
	match := selectRe.FindSubmatch(page)
	if match == nil {
		t.Fatal("objectKind select not found")
	}
	optionRe := regexp.MustCompile(`<option value="([^"]+)">`)
	var values []string
	for _, m := range optionRe.FindAllSubmatch(match[1], -1) {
		values = append(values, string(m[1]))
	}
	for _, v := range values {
		for _, loc := range []string{"site", "building", "telecom_room", "rack"} {
			if v == loc {
				t.Fatalf("item picker must not offer %s, got %v", loc, values)
			}
		}
	}
	for _, want := range []string{"device", "patch_panel", "conduit", "manhole", "label", "shape"} {
		found := false
		for _, v := range values {
			if v == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("item picker lost %q, got %v", want, values)
		}
	}
}

func TestSiteObjectsStillCreatableViaAPI(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"s1","kind":"site","name":"HQ","site_role":"major_site","x":0,"y":0}`, "s")
	if w.Code != 204 {
		t.Fatalf("create site status=%d %s", w.Code, w.Body.String())
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM objects WHERE id='s1' AND kind='site'`).Scan(&name); err != nil || name != "HQ" {
		t.Fatalf("site not stored: %q %v", name, err)
	}
	// Items reference their site for location and must name a square.
	w = apiRequest(a, http.MethodPost, "/api/objects", `{"id":"d9","kind":"device","name":"sw","device_type_id":"switch","site_id":"s1","building_id":"B1","x":0,"y":0}`, "s")
	if w.Code != 204 {
		t.Fatalf("create device with site status=%d %s", w.Code, w.Body.String())
	}
}
