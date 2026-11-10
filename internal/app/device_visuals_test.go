package app

import (
	"encoding/json"
	"image"
	_ "image/png"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestCiscoCatalogMigrationAndIconAssets(t *testing.T) {
	a, db := editorApp(t, "admin")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM device_types WHERE enabled=1`).Scan(&count); err != nil || count < 100 {
		t.Fatal(count, err)
	}
	w := apiRequest(a, http.MethodGet, "/api/device-visuals", "", "s")
	var visuals []deviceVisual
	if err := json.Unmarshal(w.Body.Bytes(), &visuals); err != nil || len(visuals) < 100 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, visual := range visuals {
		if visual.Icon == "" {
			t.Fatalf("visual %s has no icon", visual.ID)
		}
		info, err := os.Stat("../../web" + visual.Icon)
		if err != nil || info.Size() == 0 {
			t.Fatal(visual, err)
		}
		if strings.HasSuffix(visual.Icon, ".png") {
			f, err := os.Open("../../web" + visual.Icon)
			if err != nil {
				t.Fatal(err)
			}
			img, _, err := image.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			transparent := false
			b := img.Bounds()
			for y := b.Min.Y; y < b.Max.Y && !transparent; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					_, _, _, alpha := img.At(x, y).RGBA()
					if alpha == 0 {
						transparent = true
						break
					}
				}
			}
			if !transparent {
				t.Fatalf("icon %s has an opaque background", visual.ID)
			}
		}
	}
	body := `{"id":"custom-switch","name":"Custom core switch","object_kind":"device","visual":"nexus-7000"}`
	if w := apiRequest(a, http.MethodPost, "/api/device-types", body, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/objects", `{"id":"core","kind":"device","building_id":"B1","name":"Core","device_type_id":"custom-switch"}`, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data string
	_ = db.QueryRow(`SELECT data FROM objects WHERE id='core'`).Scan(&data)
	if !strings.Contains(data, `"device_visual":"nexus-7000"`) {
		t.Fatal(data)
	}
}
