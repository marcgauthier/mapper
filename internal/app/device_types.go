package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var deviceTypeKinds = map[string]bool{"device": true, "virtual_machine": true, "virtual_switch": true, "patch_panel": true}

type deviceType struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ObjectKind string `json:"object_kind"`
	Visual     string `json:"visual"`
	Medium     string `json:"medium"`
	SortOrder  int    `json:"sort_order"`
	Enabled    bool   `json:"enabled"`
	CanHostVMs bool   `json:"can_host_vms"`
}

func (a *App) deviceTypes(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == http.MethodGet {
		rows, err := db.Query(`SELECT id,name,object_kind,visual,medium,sort_order,enabled FROM device_types WHERE enabled=1 ORDER BY sort_order,name`)
		if err != nil {
			http.Error(w, "device type catalog unavailable", 500)
			return
		}
		defer rows.Close()
		out := []deviceType{}
		for rows.Next() {
			var item deviceType
			var enabled int
			if rows.Scan(&item.ID, &item.Name, &item.ObjectKind, &item.Visual, &item.Medium, &item.SortOrder, &enabled) == nil {
				item.Enabled = enabled != 0
				item.CanHostVMs = item.ObjectKind == "device" && isHypervisorMeta(map[string]any{"device_type_id": item.ID, "device_type": item.Name, "device_visual": item.Visual})
				out = append(out, item)
			}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if p.Role != "admin" {
		http.Error(w, "administrator required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "device type id required", http.StatusBadRequest)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		var typeName string
		if err = tx.QueryRow(`SELECT name FROM device_types WHERE id=?`, id).Scan(&typeName); err != nil {
			http.Error(w, "device type not found", 404)
			return
		}
		var used int
		if err = tx.QueryRow(`SELECT count(*) FROM objects WHERE json_extract(data,'$.device_type_id')=? OR json_extract(data,'$.device_type')=?`, id, typeName).Scan(&used); err != nil {
			http.Error(w, "device type reference check failed", 500)
			return
		}
		if used != 0 {
			http.Error(w, "device type is in use; edit it or reassign its objects before deleting it", http.StatusConflict)
			return
		}
		result, err := tx.Exec(`DELETE FROM device_types WHERE id=?`, id)
		if err != nil {
			http.Error(w, "device type delete failed", 500)
			return
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			http.Error(w, "device type not found", 404)
			return
		}
		if err = a.recordAudit(tx, p.User, "device_type_delete", id, map[string]any{"id": id}); err != nil {
			http.Error(w, "device type audit failed", 500)
			return
		}
		event := []byte(fmt.Sprintf(`{"type":"device_types","action":"delete","id":%q}`, id))
		seq, err := appendEvent(tx, event)
		if err != nil || a.commitLogged(tx) != nil {
			http.Error(w, "device type update failed", 500)
			return
		}
		a.notify(seq, event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var item deviceType
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item) != nil {
		http.Error(w, "invalid device type", http.StatusBadRequest)
		return
	}
	item.ID = strings.TrimSpace(strings.ToLower(item.ID))
	item.Name = strings.TrimSpace(item.Name)
	item.ObjectKind = strings.TrimSpace(item.ObjectKind)
	item.Visual = strings.TrimSpace(item.Visual)
	item.Medium = strings.TrimSpace(strings.ToLower(item.Medium))
	if !validDeviceTypeID(item.ID) || item.Name == "" || len(item.Name) > 80 || !deviceTypeKinds[item.ObjectKind] || !deviceTypeVisuals[item.Visual] || (item.Medium != "" && item.Medium != "copper" && item.Medium != "fiber") {
		http.Error(w, "device type requires a valid id, name, category, visual style, and optional copper/fiber medium", http.StatusBadRequest)
		return
	}
	if item.SortOrder < 0 || item.SortOrder > 10000 {
		http.Error(w, "sort_order must be between 0 and 10000", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && item.SortOrder == 0 {
		item.SortOrder = 100
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	action := "device_type_create"
	var before any
	if r.Method == http.MethodPost {
		_, err = tx.Exec(`INSERT INTO device_types(id,name,object_kind,visual,medium,sort_order,enabled) VALUES(?,?,?,?,?,?,1)`, item.ID, item.Name, item.ObjectKind, item.Visual, item.Medium, item.SortOrder)
	} else {
		action = "device_type_update"
		var old deviceType
		var enabled int
		if err = tx.QueryRow(`SELECT id,name,object_kind,visual,medium,sort_order,enabled FROM device_types WHERE id=?`, item.ID).Scan(&old.ID, &old.Name, &old.ObjectKind, &old.Visual, &old.Medium, &old.SortOrder, &enabled); err == nil {
			old.Enabled = enabled != 0
			before = old
			if old.ObjectKind != item.ObjectKind {
				var used int
				if err = tx.QueryRow(`SELECT count(*) FROM objects WHERE json_extract(data,'$.device_type_id')=? OR json_extract(data,'$.device_type')=?`, item.ID, old.Name).Scan(&used); err == nil && used != 0 {
					http.Error(w, "cannot change the category of a device type that is in use", http.StatusConflict)
					return
				}
			}
			if err == nil && !isHypervisorMeta(map[string]any{"device_type_id": item.ID, "device_type": item.Name, "device_visual": item.Visual}) {
				var hosted int
				if err = tx.QueryRow(`SELECT count(*) FROM objects child JOIN objects host ON host.id=json_extract(child.data,'$.host_id') WHERE child.kind IN ('virtual_machine','virtual_switch') AND (json_extract(host.data,'$.device_type_id')=? OR json_extract(host.data,'$.device_type')=?)`, item.ID, old.Name).Scan(&hosted); err == nil && hosted > 0 {
					http.Error(w, "move or remove hosted VMs and vSwitches before removing hypervisor support from this type", 409)
					return
				}
			}
			if err == nil && old.Visual != item.Visual {
				rows, readErr := tx.Query(`SELECT id,kind,data FROM objects WHERE json_extract(data,'$.device_type_id')=? OR json_extract(data,'$.device_type')=?`, item.ID, old.Name)
				if readErr != nil {
					http.Error(w, "VRF reference check failed", 500)
					return
				}
				type entry struct{ id, kind, raw string }
				var affected []entry
				for rows.Next() {
					var row entry
					if readErr = rows.Scan(&row.id, &row.kind, &row.raw); readErr != nil {
						break
					}
					affected = append(affected, row)
				}
				if readErr == nil {
					readErr = rows.Err()
				}
				rows.Close()
				if readErr != nil {
					http.Error(w, "VRF reference check failed", 500)
					return
				}
				for _, row := range affected {
					var meta map[string]any
					_ = json.Unmarshal([]byte(row.raw), &meta)
					if meta == nil {
						meta = map[string]any{}
					}
					meta["device_visual"] = item.Visual
					if checkErr := normalizeDeviceVRFs(tx, row.id, row.kind, meta); checkErr != nil {
						http.Error(w, checkErr.Error(), 409)
						return
					}
				}
			}
			if err == nil {
				_, err = tx.Exec(`UPDATE device_types SET name=?,object_kind=?,visual=?,medium=?,sort_order=? WHERE id=?`, item.Name, item.ObjectKind, item.Visual, item.Medium, item.SortOrder, item.ID)
			}
			if err == nil {
				_, err = tx.Exec(`UPDATE objects SET data=json_set(data,'$.device_type_id',?,'$.device_type',?,'$.device_visual',?,'$.medium',?),version=version+1 WHERE json_extract(data,'$.device_type_id')=? OR json_extract(data,'$.device_type')=?`, item.ID, item.Name, item.Visual, item.Medium, item.ID, old.Name)
			}
		}
	}
	if err != nil {
		http.Error(w, "device type save failed; check for duplicate IDs or names", http.StatusConflict)
		return
	}
	if err = a.recordAudit(tx, p.User, action, item.ID, map[string]any{"before": before, "after": item}); err != nil {
		http.Error(w, "device type audit failed", 500)
		return
	}
	event := []byte(fmt.Sprintf(`{"type":"device_types","action":%q,"id":%q}`, action, item.ID))
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "device type event failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "device type update failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}

func validDeviceTypeID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return false
	}
	return true
}

func deviceTypeByID(db *sql.DB, id string) (deviceType, error) {
	var item deviceType
	var enabled int
	err := db.QueryRow(`SELECT id,name,object_kind,visual,medium,sort_order,enabled FROM device_types WHERE id=?`, id).Scan(&item.ID, &item.Name, &item.ObjectKind, &item.Visual, &item.Medium, &item.SortOrder, &enabled)
	item.Enabled = enabled != 0
	item.CanHostVMs = item.ObjectKind == "device" && isHypervisorMeta(map[string]any{"device_type_id": item.ID, "device_type": item.Name, "device_visual": item.Visual})
	return item, err
}
