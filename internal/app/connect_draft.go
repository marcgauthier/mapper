package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
)

// A container endpoint owns a new DVO. No generated record exists until the
// complete connection has passed validation and its transaction commits.
type draftEndpoint struct {
	ObjectID      string `json:"object_id"`
	ContainerID   string `json:"container_id"`
	ProvisionalID string `json:"provisional_id"`
	Version       int    `json:"version"`
	InterfaceID   string `json:"interface_id"`
	InterfaceName string `json:"interface_name"`
	Revision      string `json:"revision"`
	AutoInterface bool   `json:"auto_interface"`
}
type resolvedDraftEndpoint struct {
	ObjectID          string   `json:"object_id"`
	InterfaceID       string   `json:"interface_id"`
	CreatedObjects    []string `json:"created_objects"`
	CreatedInterfaces []string `json:"created_interfaces"`
}

func resolveDraftEndpoint(tx *sql.Tx, endpoint draftEndpoint) (resolvedDraftEndpoint, error) {
	out := resolvedDraftEndpoint{CreatedObjects: []string{}, CreatedInterfaces: []string{}}
	id := endpoint.ObjectID
	if endpoint.ContainerID != "" {
		if id != "" || endpoint.InterfaceID != "" || endpoint.AutoInterface {
			return out, fmt.Errorf("invalid container endpoint")
		}
		id = endpoint.ContainerID
	}
	var kind, name, raw string
	var version int
	if id == "" || tx.QueryRow(`SELECT kind,name,data,version FROM objects WHERE id=?`, id).Scan(&kind, &name, &raw, &version) != nil {
		return out, fmt.Errorf("endpoint was removed; choose it again")
	}
	if endpoint.Version < 1 || endpoint.Version != version {
		return out, fmt.Errorf("endpoint changed; refresh its choices")
	}
	var owner map[string]any
	if err := json.Unmarshal([]byte(raw), &owner); err != nil {
		return out, err
	}
	if endpoint.ContainerID != "" {
		if !topologyLocationMap(kind) {
			return out, fmt.Errorf("DVO requires a site, building, floor, room or rack")
		}
		key := map[string]string{"site": "site_id", "building": "building_id", "floor": "floor_id", "room": "room_id", "telecom_room": "telecom_room_id", "rack": "rack_id"}[kind]
		dvo := endpoint.ProvisionalID
		if dvo == "" {
			dvo = newID()
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM objects WHERE kind='outlet' AND json_extract(data,'$.`+key+`')=?`, id).Scan(&count); err != nil {
			return out, err
		}
		meta := map[string]any{"outlet_type": "dvo", key: id}
		if err := normalizeTopologyPlacement(tx, dvo, "outlet", meta); err != nil {
			return out, err
		}
		meta["id"], meta["kind"], meta["name"] = dvo, "outlet", fmt.Sprintf("DVO %d", count+1)
		encoded, _ := json.Marshal(meta)
		if _, err := tx.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,'outlet',?,?,0,0)`, dvo, meta["name"], string(encoded)); err != nil {
			return out, fmt.Errorf("DVO identifier is already used")
		}
		if err := ensureTopologyPorts(tx, dvo, "outlet", meta); err != nil {
			return out, err
		}
		portName := endpoint.InterfaceName
		if portName == "" {
			portName = "Data"
		}
		if portName != "Data" && portName != "Voice" {
			return out, fmt.Errorf("choose Data or Voice for the DVO")
		}
		if err := tx.QueryRow(`SELECT id FROM interfaces WHERE device_id=? AND name=?`, dvo, portName).Scan(&out.InterfaceID); err != nil {
			return out, err
		}
		rows, err := tx.Query(`SELECT id FROM interfaces WHERE device_id=? ORDER BY name`, dvo)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var port string
			if err = rows.Scan(&port); err != nil {
				break
			}
			out.CreatedInterfaces = append(out.CreatedInterfaces, port)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if rowErr != nil {
			return out, rowErr
		}
		out.ObjectID = dvo
		out.CreatedObjects = append(out.CreatedObjects, dvo)
		return out, nil
	}
	if !interfaceOwnerKindAllowed(kind) {
		return out, fmt.Errorf("choose equipment or an outlet")
	}
	out.ObjectID = id
	if endpoint.AutoInterface {
		if endpoint.InterfaceID != "" {
			return out, fmt.Errorf("choose an existing interface or a new default, not both")
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM interfaces WHERE device_id=?`, id).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 {
			return out, fmt.Errorf("interfaces were added by another user; refresh interface choices")
		}
		port := map[string]any{}
		if err := normalizeInterfaceVRFs(owner, port); err != nil {
			return out, err
		}
		if _, err := normalizeInterfaceCapacity(kind, owner, port, false); err != nil {
			return out, err
		}
		encoded, _ := json.Marshal(port)
		out.InterfaceID = newID()
		if _, err := tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,'Interface 1',?)`, out.InterfaceID, id, string(encoded)); err != nil {
			return out, err
		}
		out.CreatedInterfaces = append(out.CreatedInterfaces, out.InterfaceID)
		return out, nil
	}
	var portName, portRaw string
	if endpoint.InterfaceID == "" || tx.QueryRow(`SELECT name,data FROM interfaces WHERE id=? AND device_id=?`, endpoint.InterfaceID, id).Scan(&portName, &portRaw) != nil {
		return out, fmt.Errorf("selected interface was removed or belongs to another item")
	}
	if endpoint.Revision != "" && interfaceRevision(endpoint.InterfaceID, id, portName, portRaw, raw) != endpoint.Revision {
		return out, fmt.Errorf("selected interface changed; refresh its choices")
	}
	out.InterfaceID = endpoint.InterfaceID
	return out, nil
}
func (a *App) connectDraft(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if p.Role == "viewer" {
		http.Error(w, "editor required", 403)
		return
	}
	var input struct {
		ID     string        `json:"id"`
		Source draftEndpoint `json:"source"`
		Target draftEndpoint `json:"target"`
		Data   any           `json:"data"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid connection draft", 400)
		return
	}
	if input.ID == "" {
		input.ID = newID()
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	spec, err := validateLinkSpecWithCustoms(input.Data, func(class, id string) bool { return customConnectionSubtype(tx, class, id) })
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	source, err := resolveDraftEndpoint(tx, input.Source)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	target, err := resolveDraftEndpoint(tx, input.Target)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if source.InterfaceID == target.InterfaceID || source.ObjectID == target.ObjectID && spec["connection_class"] == "physical" {
		http.Error(w, "choose distinct interfaces on different equipment for a physical cable", 400)
		return
	}
	if err = checkLinkAvailability(tx, source.InterfaceID, target.InterfaceID, spec, ""); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	encoded, _ := json.Marshal(spec)
	if _, err = tx.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES(?,?,?,?)`, input.ID, source.InterfaceID, target.InterfaceID, string(encoded)); err != nil {
		http.Error(w, "connection could not be saved; refresh endpoints", 409)
		return
	}
	// Generated records are audited in the same transaction as their link.
	for _, endpoint := range []resolvedDraftEndpoint{source, target} {
		for _, id := range endpoint.CreatedObjects {
			var raw string
			if err = tx.QueryRow(`SELECT data FROM objects WHERE id=?`, id).Scan(&raw); err != nil {
				http.Error(w, "generated object lookup failed", 500)
				return
			}
			var data any
			_ = json.Unmarshal([]byte(raw), &data)
			if err = a.recordAudit(tx, p.User, "connection_object_create", id, data); err != nil {
				http.Error(w, "audit write failed", 500)
				return
			}
		}
		for _, id := range endpoint.CreatedInterfaces {
			var owner, name, raw string
			if err = tx.QueryRow(`SELECT device_id,name,data FROM interfaces WHERE id=?`, id).Scan(&owner, &name, &raw); err != nil {
				http.Error(w, "generated interface lookup failed", 500)
				return
			}
			var data any
			_ = json.Unmarshal([]byte(raw), &data)
			if err = a.recordAudit(tx, p.User, "interface_create", id, map[string]any{"device_id": owner, "name": name, "data": data}); err != nil {
				http.Error(w, "audit write failed", 500)
				return
			}
		}
	}
	audit := map[string]any{"id": input.ID, "source": source, "target": target, "data": spec}
	if err = a.recordAudit(tx, p.User, "link_create", input.ID, audit); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]any{"type": "connection_create", "id": input.ID, "source": source, "target": target})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err = a.commitLogged(tx); err != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, event)
	json.NewEncoder(w).Encode(map[string]any{"id": input.ID, "source": source, "target": target, "sequence": seq})
}
