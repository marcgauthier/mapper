package app

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

func validateShapeSpec(data map[string]any) error {
	kind, _ := data["shape_type"].(string)
	if value, exists := data["shape_type"]; exists && value != kind {
		return fmt.Errorf("shape_type must be text")
	}
	if kind == "" {
		kind = "rectangle"
	}
	if kind != "square" && kind != "rectangle" && kind != "circle" && kind != "cloud" {
		return fmt.Errorf("shape_type must be square, rectangle, circle, or cloud")
	}
	data["shape_type"] = kind
	for key, fallback := range map[string]float64{"width": 160, "height": 100} {
		n := fallback
		if value, ok := data[key]; ok {
			var err error
			n, err = strconv.ParseFloat(fmt.Sprint(value), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 24 || n > 2000 {
				return fmt.Errorf("%s must be between 24 and 2000", key)
			}
		}
		data[key] = n
	}
	if kind == "square" || kind == "circle" {
		data["height"] = data["width"]
	}
	color, exists := data["fill_color"]
	if !exists {
		data["fill_color"] = "#dbeafe"
		return nil
	}
	text, ok := color.(string)
	if !ok || len(text) != 7 || text[0] != '#' {
		return fmt.Errorf("fill_color must be a #RRGGBB color")
	}
	if _, err := hex.DecodeString(text[1:]); err != nil {
		return fmt.Errorf("fill_color must be a #RRGGBB color")
	}
	return nil
}

// Shapes have one shared connection point. It accepts many edges, while each
// normal interface remains bound to a single peer item.
func ensureShapeInterface(tx *sql.Tx, objectID string) error {
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=?`, objectID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?)`, newID(), objectID, "Connection point", "{}")
	return err
}

func interfaceOwnerKindAllowed(kind string) bool {
	switch kind {
	case "device", "virtual_machine", "virtual_switch", "patch_panel", "outlet", "service", "shape", "label", "manhole":
		return true
	}
	return false
}

func checkShapeInterface(tx *sql.Tx, owner, id string) error {
	var other int
	if err := tx.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=? AND id<>?`, owner, id).Scan(&other); err != nil {
		return err
	}
	if other > 0 {
		return fmt.Errorf("a shape has exactly one shared connection point")
	}
	return nil
}
