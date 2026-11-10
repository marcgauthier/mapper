package main

// Retain legacy metadata for lossless export/restore without a geographic
// renderer, editing API, or constraints on the active Cytoscape diagram.
import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type archivedGeographyRecord struct {
	Type    string          `json:"owner_type"`
	ID      string          `json:"id"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

func importArchivedGeography(tx *sql.Tx, g *archivedGeographyRecord) error {
	if (g.Type != "object" && g.Type != "link") || g.ID == "" || len(g.ID) > 128 {
		return fmt.Errorf("invalid archived geographic owner")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(g.Data, &data); err != nil || data == nil {
		return fmt.Errorf("archived geographic data must be a JSON object")
	}
	reference := func(key string) (string, error) {
		raw, ok := data[key]
		if !ok {
			return "", nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("invalid archived %s", key)
		}
		return value, nil
	}
	building, err := reference("building_id")
	if err != nil {
		return err
	}
	floor, err := reference("floor_id")
	if err != nil {
		return err
	}
	parent, err := reference("parent_id")
	if err != nil {
		return err
	}
	if g.Version < 1 {
		g.Version = 1
	}
	// Bounds are no longer queried; preserve the payload and archive indexes only.
	_, err = tx.Exec(`INSERT INTO geography(owner_type,owner_id,data,version,building_id,floor_id,parent_id,min_x,min_y,max_x,max_y) VALUES(?,?,?,?,?,?,?,0,0,0,0) ON CONFLICT(owner_type,owner_id) DO UPDATE SET data=excluded.data,version=geography.version+1,building_id=excluded.building_id,floor_id=excluded.floor_id,parent_id=excluded.parent_id`, g.Type, g.ID, string(g.Data), g.Version, building, floor, parent)
	return err
}
