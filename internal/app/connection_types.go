package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

var connectionTypeClasses = map[string]bool{"physical": true, "virtual": true, "logical_tunnel": true, "provider_service": true}

// builtinConnectionSubtypes reserves the validation codes handled in Go so a
// custom type can never shadow or redefine built-in behavior. "other" stays
// reserved in every class as the one-off free-text escape hatch.
var builtinConnectionSubtypes = map[string]map[string]bool{
	"physical":         {"fiber": true, "copper_twisted_pair": true, "copper": true, "twisted_pair": true, "fibre": true, "optical": true, "coax": true, "twinax_dac": true, "wireless_radio": true, "backplane": true, "other": true},
	"virtual":          {"ethernet": true, "vlan": true, "bridge": true, "veth": true, "port_group": true, "overlay": true, "bond": true, "lag": true, "loopback": true, "other": true},
	"logical_tunnel":   {"ipsec": true, "wireguard": true, "gre": true, "vxlan": true, "l2tp": true, "geneve": true, "ipip": true, "sit": true, "openvpn": true, "ssl_vpn": true, "other": true},
	"provider_service": {"pls": true, "leased_circuit": true, "dark_fiber": true, "wavelength": true, "carrier_ethernet": true, "mpls": true, "internet": true, "evpn": true, "sd_wan": true, "other": true},
}

type connectionType struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ConnectionClass string `json:"connection_class"`
	Color           string `json:"color"`
	SortOrder       int    `json:"sort_order"`
	Enabled         bool   `json:"enabled"`
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func customConnectionSubtype(q queryRower, class, id string) bool {
	var found string
	if q.QueryRow(`SELECT id FROM connection_types WHERE connection_class=? AND id=? AND enabled=1`, class, id).Scan(&found) != nil {
		return false
	}
	return true
}

func connectionTypeInUse(q queryRower, class, id string) bool {
	var predicate string
	switch class {
	case "physical":
		predicate = `json_extract(data,'$.connection_class')='physical' AND (json_extract(data,'$.physical_type')=? OR json_extract(data,'$.medium')=?)`
		var used int
		if q.QueryRow(`SELECT count(*) FROM links WHERE `+predicate, id, id).Scan(&used) != nil {
			return false
		}
		return used != 0
	case "virtual":
		predicate = `json_extract(data,'$.connection_class')='virtual' AND json_extract(data,'$.virtual_type')=?`
	case "logical_tunnel":
		predicate = `json_extract(data,'$.connection_class')='logical_tunnel' AND lower(json_extract(data,'$.tunnel_protocol'))=?`
	case "provider_service":
		predicate = `json_extract(data,'$.connection_class')='provider_service' AND json_extract(data,'$.service_type')=?`
	default:
		return false
	}
	var used int
	if q.QueryRow(`SELECT count(*) FROM links WHERE `+predicate, id).Scan(&used) != nil {
		return false
	}
	return used != 0
}

func validConnectionColor(color string) bool {
	if color == "" {
		return true
	}
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for _, r := range color[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func (a *App) connectionTypes(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method == http.MethodGet {
		rows, err := db.Query(`SELECT id,name,connection_class,color,sort_order,enabled FROM connection_types WHERE enabled=1 ORDER BY connection_class,sort_order,name`)
		if err != nil {
			http.Error(w, "connection type catalog unavailable", 500)
			return
		}
		defer rows.Close()
		out := []connectionType{}
		for rows.Next() {
			var item connectionType
			var enabled int
			if rows.Scan(&item.ID, &item.Name, &item.ConnectionClass, &item.Color, &item.SortOrder, &enabled) == nil {
				item.Enabled = enabled != 0
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
		id := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("id")))
		if id == "" {
			http.Error(w, "connection type id required", http.StatusBadRequest)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "database busy", 503)
			return
		}
		defer a.rollbackLogged(tx)
		var class string
		if err = tx.QueryRow(`SELECT connection_class FROM connection_types WHERE id=?`, id).Scan(&class); err != nil {
			http.Error(w, "connection type not found", 404)
			return
		}
		if connectionTypeInUse(tx, class, id) {
			http.Error(w, "connection type is in use; delete its connections before deleting it", http.StatusConflict)
			return
		}
		result, err := tx.Exec(`DELETE FROM connection_types WHERE id=?`, id)
		if err != nil {
			http.Error(w, "connection type delete failed", 500)
			return
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			http.Error(w, "connection type not found", 404)
			return
		}
		if err = a.recordAudit(tx, p.User, "connection_type_delete", id, map[string]any{"id": id}); err != nil {
			http.Error(w, "connection type audit failed", 500)
			return
		}
		event := []byte(fmt.Sprintf(`{"type":"connection_types","action":"delete","id":%q}`, id))
		seq, err := appendEvent(tx, event)
		if err != nil || a.commitLogged(tx) != nil {
			http.Error(w, "connection type update failed", 500)
			return
		}
		a.notify(seq, event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var item connectionType
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&item) != nil {
		http.Error(w, "invalid connection type", http.StatusBadRequest)
		return
	}
	item.ID = strings.TrimSpace(strings.ToLower(item.ID))
	item.Name = strings.TrimSpace(item.Name)
	item.ConnectionClass = strings.TrimSpace(item.ConnectionClass)
	item.Color = strings.TrimSpace(item.Color)
	if !validDeviceTypeID(item.ID) || item.Name == "" || len(item.Name) > 80 || !connectionTypeClasses[item.ConnectionClass] || !validConnectionColor(item.Color) {
		http.Error(w, "connection type requires a valid id, name, category, and optional #RRGGBB map color", http.StatusBadRequest)
		return
	}
	if builtinConnectionSubtypes[item.ConnectionClass][item.ID] {
		http.Error(w, "connection type id is reserved by a built-in type", http.StatusConflict)
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
	action := "connection_type_create"
	var before any
	if r.Method == http.MethodPost {
		_, err = tx.Exec(`INSERT INTO connection_types(id,name,connection_class,color,sort_order,enabled) VALUES(?,?,?,?,?,1)`, item.ID, item.Name, item.ConnectionClass, item.Color, item.SortOrder)
	} else {
		action = "connection_type_update"
		var old connectionType
		var enabled int
		if err = tx.QueryRow(`SELECT id,name,connection_class,color,sort_order,enabled FROM connection_types WHERE id=?`, item.ID).Scan(&old.ID, &old.Name, &old.ConnectionClass, &old.Color, &old.SortOrder, &enabled); err == nil {
			old.Enabled = enabled != 0
			before = old
			if old.ConnectionClass != item.ConnectionClass && connectionTypeInUse(tx, old.ConnectionClass, item.ID) {
				http.Error(w, "cannot change the category of a connection type that is in use", http.StatusConflict)
				return
			}
			if err == nil {
				_, err = tx.Exec(`UPDATE connection_types SET name=?,connection_class=?,color=?,sort_order=? WHERE id=?`, item.Name, item.ConnectionClass, item.Color, item.SortOrder, item.ID)
			}
		}
	}
	if err != nil {
		http.Error(w, "connection type save failed; check for duplicate IDs or names", http.StatusConflict)
		return
	}
	if err = a.recordAudit(tx, p.User, action, item.ID, map[string]any{"before": before, "after": item}); err != nil {
		http.Error(w, "connection type audit failed", 500)
		return
	}
	event := []byte(fmt.Sprintf(`{"type":"connection_types","action":%q,"id":%q}`, action, item.ID))
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "connection type event failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "connection type update failed", 500)
		return
	}
	a.notify(seq, event)
	w.WriteHeader(http.StatusNoContent)
}
