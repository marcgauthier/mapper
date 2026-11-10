package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Analysis reads raw assignments as well as effective VRFs so stale legacy
// references remain visible even though current writers reject them.
type analysisPort struct {
	ID          string   `json:"id"`
	DeviceID    string   `json:"device_id"`
	Device      string   `json:"device"`
	Name        string   `json:"name"`
	Networks    []string `json:"networks"`
	Network     string   `json:"network"`
	VRFs        []string `json:"vrfs"`
	IP          string   `json:"ip,omitempty"`
	Kind        string   `json:"kind"`
	Role        string   `json:"-"`
	MissingVRFs []string `json:"missing_vrfs,omitempty"`
}
type networkAnalysis struct {
	Ports    []analysisPort
	Sequence int64
}

func loadNetworkAnalysis(db *sql.DB) (networkAnalysis, error) {
	out := networkAnalysis{Ports: []analysisPort{}}
	tx, err := db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out, err = loadNetworkAnalysisTx(tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func loadNetworkAnalysisTx(tx *sql.Tx) (networkAnalysis, error) {
	out := networkAnalysis{Ports: []analysisPort{}}
	var err error
	if err = tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&out.Sequence); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT i.id,i.device_id,i.name,i.data,o.name,o.data,o.kind FROM interfaces i JOIN objects o ON o.id=i.device_id ORDER BY i.id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p analysisPort
		var raw, ownerRaw string
		if err = rows.Scan(&p.ID, &p.DeviceID, &p.Name, &raw, &p.Device, &ownerRaw, &p.Kind); err != nil {
			return out, err
		}
		var meta, owner map[string]any
		if err = json.Unmarshal([]byte(raw), &meta); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(ownerRaw), &owner); err != nil {
			return out, err
		}
		p.Role = vrfRole(owner)
		p.Networks = analysisNetworks(owner["network"])
		p.Network = strings.Join(p.Networks, ", ")
		p.IP = topologyText(meta, "ip")
		if p.IP == "" {
			p.IP = topologyText(meta, "ip_address")
		}
		p.VRFs = effectiveInterfaceVRFs(owner, meta)
		if p.VRFs == nil {
			p.VRFs = []string{}
		}
		declared, _ := vrfNames(owner["vrfs"])
		assigned, _ := vrfNames(meta["vrfs"])
		known := map[string]bool{}
		for _, name := range declared {
			known[name] = true
		}
		if topologyText(meta, "vrf_mode") != "all" {
			for _, name := range assigned {
				if !known[name] {
					p.MissingVRFs = append(p.MissingVRFs, name)
				}
			}
		}
		out.Ports = append(out.Ports, p)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	return out, nil
}

type qualityIssue struct {
	PortCount int            `json:"port_count"`
	Kind      string         `json:"kind"`
	Address   string         `json:"address,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Ports     []analysisPort `json:"ports"`
}

func canonicalInterfaceIP(value string) (string, error) {
	if p, e := netip.ParsePrefix(value); e == nil {
		return p.Addr().Unmap().String(), nil
	}
	a, e := netip.ParseAddr(value)
	if e != nil {
		return "", e
	}
	return a.Unmap().String(), nil
}
func qualityIssues(ports []analysisPort) []qualityIssue {
	out := []qualityIssue{}
	addresses := map[string][]analysisPort{}
	for _, p := range ports {
		if len(p.MissingVRFs) > 0 {
			out = append(out, qualityIssue{Kind: "deleted_vrf", Ports: []analysisPort{p}})
		}
		if p.IP == "" {
			continue
		}
		ip, e := canonicalInterfaceIP(p.IP)
		if e != nil {
			out = append(out, qualityIssue{Kind: "invalid_ip", Ports: []analysisPort{p}})
			continue
		}
		addresses[ip] = append(addresses[ip], p)
	}
	keys := []string{}
	for ip := range addresses {
		keys = append(keys, ip)
	}
	sort.Strings(keys)
	for _, ip := range keys {
		peers := addresses[ip]
		if len(peers) < 2 {
			continue
		}
		out = append(out, qualityIssue{Kind: "address_reuse", Address: ip, Ports: peers})
		scopes := map[string][]analysisPort{}
		for _, p := range peers {
			vrfs := p.VRFs
			if len(vrfs) == 0 {
				vrfs = []string{""}
			}
			networks := p.Networks
			if len(networks) == 0 {
				networks = analysisNetworks(p.Network)
			}
			if len(networks) == 0 {
				networks = []string{""}
			}
			for _, n := range networks {
				for _, v := range vrfs {
					key := fmt.Sprintf("%q / %q", n, v)
					scopes[key] = append(scopes[key], p)
				}
			}
		}

		names := []string{}
		for name := range scopes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if len(scopes[name]) > 1 {
				out = append(out, qualityIssue{Kind: "duplicate_ip", Address: ip, Scope: name, Ports: scopes[name]})
			}
		}
	}
	return out
}

func (a *App) networkQuality(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset = 0
		err = nil
	}
	if err != nil || offset < 0 {
		http.Error(w, "invalid offset", 400)
		return
	}
	data, err := loadNetworkAnalysis(db)
	if err != nil {
		http.Error(w, "network analysis failed", 500)
		return
	}
	if expected := r.URL.Query().Get("sequence"); expected != "" && expected != strconv.FormatInt(data.Sequence, 10) {
		http.Error(w, "inventory changed; refresh findings", 409)
		return
	}
	issues := qualityIssues(data.Ports)
	if kind := r.URL.Query().Get("kind"); kind != "" {
		filtered := []qualityIssue{}
		for _, issue := range issues {
			if issue.Kind == kind && issue.Address == r.URL.Query().Get("address") && issue.Scope == r.URL.Query().Get("scope") {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}
	portOffset := 0
	if raw := r.URL.Query().Get("port_offset"); raw != "" {
		portOffset, err = strconv.Atoi(raw)
		if err != nil || portOffset < 0 {
			http.Error(w, "invalid port offset", 400)
			return
		}
	}

	total := len(issues)
	if offset > total {
		offset = total
	}
	end := offset + 100
	if end > total {
		end = total
	}
	page := issues[offset:end]
	for i := range page {
		page[i].PortCount = len(page[i].Ports)
		start := portOffset
		if start > len(page[i].Ports) {
			start = len(page[i].Ports)
		}
		end := start + 100
		if end > len(page[i].Ports) {
			end = len(page[i].Ports)
		}
		page[i].Ports = page[i].Ports[start:end]
	}
	json.NewEncoder(w).Encode(map[string]any{"sequence": data.Sequence, "issues": page, "total": total, "offset": offset, "next_offset": end})
}

func containsVRF(names []string, name string) bool {
	for _, v := range names {
		if v == name {
			return true
		}
	}
	return false
}

func analysisNetworks(value any) []string {
	names := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			names = append(names, s)
		}
	}
	switch v := value.(type) {
	case string:
		for _, s := range strings.Split(v, ",") {
			add(s)
		}
	case []any:
		for _, s := range v {
			if text, ok := s.(string); ok {
				add(text)
			}
		}
	}
	sort.Strings(names)
	return names
}
