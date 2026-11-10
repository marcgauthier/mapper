package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

type vrfTraceStep struct {
	FromName string `json:"from_name"`
	ToName   string `json:"to_name"`
	From     string `json:"from_interface"`
	To       string `json:"to_interface"`
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
}

// The graph describes potential inventory connectivity, not routing tables or
// live reachability. Panel pairings transparently carry a named VRF; equipment
// hops require valid explicit/effective membership on both interfaces.
func (a *App) vrfTrace(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	q := r.URL.Query()
	start, target, vrf := q.Get("interface_id"), q.Get("target_interface_id"), q.Get("vrf")
	if start == "" || target == "" || vrf == "" {
		http.Error(w, "interface_id, target_interface_id and vrf required", 400)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer tx.Rollback()
	data, err := loadNetworkAnalysisTx(tx)
	if err != nil {
		http.Error(w, "analysis failed", 500)
		return
	}
	ports := map[string]analysisPort{}
	eligible := map[string]bool{}
	devices := map[string][]string{}
	for _, p := range data.Ports {
		ports[p.ID] = p
		member := containsVRF(p.VRFs, vrf) && !containsVRF(p.MissingVRFs, vrf)
		eligible[p.ID] = member || p.Kind == "patch_panel"
		if member && (p.Role == "router" || p.Role == "switch") {
			devices[p.DeviceID] = append(devices[p.DeviceID], p.ID)
		}
	}
	for _, id := range []string{start, target} {
		p, ok := ports[id]
		if !ok {
			http.Error(w, "endpoint not found", 404)
			return
		}
		if !containsVRF(p.VRFs, vrf) || containsVRF(p.MissingVRFs, vrf) {
			http.Error(w, "endpoint is not assigned to the selected VRF", 400)
			return
		}
	}
	graph := map[string][]vrfTraceStep{}
	add := func(from, to, kind, id string) {
		if eligible[from] && eligible[to] {
			graph[from] = append(graph[from], vrfTraceStep{From: from, To: to, Kind: kind, ID: id})
			graph[to] = append(graph[to], vrfTraceStep{From: to, To: from, Kind: kind, ID: id})
		}
	}
	rows, err := tx.Query(`SELECT id,source_interface,target_interface FROM links ORDER BY id`)
	if err != nil {
		http.Error(w, "link lookup failed", 500)
		return
	}
	for rows.Next() {
		var id, from, to string
		if err = rows.Scan(&id, &from, &to); err != nil {
			break
		}
		add(from, to, "connection", id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "link lookup failed", 500)
		return
	}
	rows, err = tx.Query(`SELECT id,port_a,port_b FROM patch_pairings ORDER BY id`)
	if err != nil {
		http.Error(w, "pairing lookup failed", 500)
		return
	}
	for rows.Next() {
		var id, from, to string
		if err = rows.Scan(&id, &from, &to); err != nil {
			break
		}
		add(from, to, "pairing", id)
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		http.Error(w, "pairing lookup failed", 500)
		return
	}
	// Expand each device once, keeping traversal linear in ports plus links.
	queue := []string{start}
	seen := map[string]bool{start: true}
	expanded := map[string]bool{}
	previous := map[string]vrfTraceStep{}
	visit := func(step vrfTraceStep) {
		if !seen[step.To] {
			seen[step.To] = true
			previous[step.To] = step
			queue = append(queue, step.To)
		}
	}
	for head := 0; head < len(queue) && !seen[target]; head++ {
		current := queue[head]
		for _, step := range graph[current] {
			visit(step)
		}
		device := ports[current].DeviceID
		if !expanded[device] && containsVRF(ports[current].VRFs, vrf) {
			expanded[device] = true
			for _, id := range devices[device] {
				visit(vrfTraceStep{From: current, To: id, Kind: "device", ID: device})
			}
		}
	}
	steps := []vrfTraceStep{}
	if seen[target] {
		for id := target; id != start; {
			step := previous[id]
			steps = append(steps, step)
			id = step.From
		}
		for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
			steps[i], steps[j] = steps[j], steps[i]
		}
	}
	for i := range steps {
		from, to := ports[steps[i].From], ports[steps[i].To]
		steps[i].FromName = from.Device + " / " + from.Name
		steps[i].ToName = to.Device + " / " + to.Name
	}
	json.NewEncoder(w).Encode(map[string]any{"sequence": data.Sequence, "vrf": vrf, "reachable": seen[target], "start_interface": start, "destination_interface": target, "steps": steps, "interpretation": "Potential inventory path; matching device-local VRF names and internal forwarding are assumed. Patch-panel pairings are transparent. Routing policy, VLAN encapsulation and operational state are not evaluated."})
}
