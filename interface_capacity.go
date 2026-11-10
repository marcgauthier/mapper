package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxInterfaceRate int64 = 9007199254740991 // exact JSON/JavaScript integer
var allocationIntegerPattern = regexp.MustCompile(`^[0-9]+(?:\.0+)?$`)
var networkRatePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([kKmMgGtTpP]?)(?:b(?:ps|/s))?$`)

func parseNetworkRate(text string) (int64, error) {
	if len(text) > 64 {
		return 0, fmt.Errorf("network rate is too long")
	}
	match := networkRatePattern.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return 0, fmt.Errorf("use a positive network speed such as 100Mbps or 1Gbps (bits per second)")
	}
	value, ok := new(big.Rat).SetString(match[1])
	if !ok {
		return 0, fmt.Errorf("invalid network rate")
	}
	multiplier := int64(1)
	switch strings.ToUpper(match[2]) {
	case "K":
		multiplier = 1000
	case "M":
		multiplier = 1000000
	case "G":
		multiplier = 1000000000
	case "T":
		multiplier = 1000000000000
	case "P":
		multiplier = 1000000000000000
	}
	value.Mul(value, new(big.Rat).SetInt64(multiplier))
	if !value.IsInt() || !value.Num().IsInt64() {
		return 0, fmt.Errorf("rate must be an exact whole number of bits per second")
	}
	rate := value.Num().Int64()
	if rate <= 0 || rate > maxInterfaceRate {
		return 0, fmt.Errorf("rate must be positive and no greater than %d bps", maxInterfaceRate)
	}
	return rate, nil
}

func interfaceIntegerRate(value any) (int64, error) {
	var rate int64
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v <= 0 || v > float64(maxInterfaceRate) {
			return 0, fmt.Errorf("bandwidth must be a positive whole number of bits per second")
		}
		rate = int64(v)
	case int64:
		rate = v
	case int:
		rate = int64(v)
	case json.Number:
		if len(v.String()) > 64 || !allocationIntegerPattern.MatchString(v.String()) {
			return 0, fmt.Errorf("bandwidth must be a whole number of bits per second")
		}
		var err error
		rate, err = strconv.ParseInt(strings.SplitN(v.String(), ".", 2)[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bandwidth is outside the supported rate range")
		}
	default:
		return 0, fmt.Errorf("bandwidth must be an integer in bits per second")
	}
	if rate <= 0 || rate > maxInterfaceRate {
		return 0, fmt.Errorf("bandwidth must be positive and no greater than %d bps", maxInterfaceRate)
	}
	return rate, nil
}

func interfaceBudgetMap(value any) (map[string]int64, error) {
	out := map[string]int64{}
	if value == nil {
		return out, nil
	}
	var entries map[string]any
	switch v := value.(type) {
	case map[string]any:
		entries = v
	case map[string]int64:
		entries = map[string]any{}
		for k, n := range v {
			entries[k] = n
		}
	default:
		return nil, fmt.Errorf("vrf_bandwidth_bps must be an object keyed by VRF name")
	}
	if len(entries) > 256 {
		return nil, fmt.Errorf("at most 256 VRF bandwidth allocations are supported per interface")
	}
	for name, value := range entries {
		rate, err := interfaceIntegerRate(value)
		if err != nil {
			return nil, fmt.Errorf("VRF %s: %w", name, err)
		}
		out[name] = rate
	}
	return out, nil
}

func effectiveInterfaceVRFs(owner, port map[string]any) []string {
	names, _ := vrfNames(port["vrfs"])
	_, explicit := port["vrfs"]
	if port["vrf_mode"] == "all" || (port["vrf_mode"] == nil && !explicit && vrfRole(owner) == "router") {
		names, _ = vrfNames(owner["vrfs"])
	}
	return names
}

func defaultInterfaceType(kind string) string {
	switch kind {
	case "virtual_machine", "virtual_switch":
		return "virtual"
	case "patch_panel", "shape", "label", "manhole", "service", "outlet":
		return "passive"
	default:
		return "physical"
	}
}

func stripInterfaceComputed(meta map[string]any) {
	for _, key := range []string{"validation_issues", "allocated_bps", "remaining_bps", "effective_vrfs", "speed_bps"} {
		delete(meta, key)
	}
}

// Strict writes require complete details; reads/imports/device edits retain unknowns.
// Invalid supplied values and over-capacity budgets are rejected in both modes.
func normalizeInterfaceCapacity(kind string, owner, meta map[string]any, strict bool) ([]string, error) {
	stripInterfaceComputed(meta)
	usage := topologyText(meta, "interface_usage")
	if usage == "" && meta["interface_usage"] == nil {
		usage = "regular"
	}
	if usage != "regular" && usage != "uplink" {
		return nil, fmt.Errorf("interface_usage must be regular or uplink")
	}
	typ := topologyText(meta, "interface_type")
	if typ == "" && meta["interface_type"] == nil {
		typ = defaultInterfaceType(kind)
	}
	switch typ {
	case "physical", "virtual", "logical", "passive":
	default:
		return nil, fmt.Errorf("invalid interface_type")
	}
	if defaultInterfaceType(kind) == "passive" && typ != "passive" {
		return nil, fmt.Errorf("passive connection points must remain passive")
	}
	if defaultInterfaceType(kind) != "passive" && typ == "passive" {
		return nil, fmt.Errorf("equipment interfaces cannot be passive connection points")
	}
	meta["interface_usage"], meta["interface_type"] = usage, typ
	var speed int64
	if v := meta["speed"]; v != nil {
		text, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("speed must be a network rate such as 1Gbps")
		}
		text = strings.TrimSpace(text)
		meta["speed"] = text
		if text != "" {
			var err error
			speed, err = parseNetworkRate(text)
			if err != nil {
				return nil, err
			}
			meta["speed_bps"] = speed
		}
	}
	budgets, err := interfaceBudgetMap(meta["vrf_bandwidth_bps"])
	if err != nil {
		return nil, err
	}
	routerUplink := vrfRole(owner) == "router" && usage == "uplink"
	if len(budgets) > 0 && !routerUplink {
		return nil, fmt.Errorf("VRF bandwidth allocations apply only to router uplinks")
	}
	names := effectiveInterfaceVRFs(owner, meta)
	carried := map[string]bool{}
	for _, name := range names {
		carried[name] = true
	}
	var total int64
	for name, rate := range budgets {
		if !carried[name] {
			return nil, fmt.Errorf("bandwidth references VRF %q which is not carried by this interface", name)
		}
		if total > maxInterfaceRate-rate {
			return nil, fmt.Errorf("total VRF bandwidth exceeds the supported rate range")
		}
		total += rate
	}
	if speed > 0 && total > speed {
		return nil, fmt.Errorf("total VRF bandwidth %d bps exceeds uplink capacity %d bps", total, speed)
	}
	if _, present := meta["vrf_bandwidth_bps"]; present {
		meta["vrf_bandwidth_bps"] = budgets
	}
	issues := []string{}
	if speed == 0 && (typ == "physical" || routerUplink && (len(names) >= 2 || len(budgets) > 0)) {
		issues = append(issues, "Interface speed is required")
	}
	if routerUplink && len(names) >= 2 {
		for _, name := range names {
			if _, found := budgets[name]; !found {
				issues = append(issues, "Missing bandwidth allocation for VRF "+name)
			}
		}
	}
	if strict && len(issues) > 0 {
		return issues, fmt.Errorf("%s", strings.Join(issues, "; "))
	}
	return issues, nil
}

func interfaceRevision(id, device, name, raw, ownerRaw string) string {
	sum := sha256.Sum256([]byte(id + "\x00" + device + "\x00" + name + "\x00" + raw + "\x00" + ownerRaw))
	return hex.EncodeToString(sum[:])
}

func decodeInterfaceMetadata(raw string) (map[string]any, error) {
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("invalid interface metadata")
	}
	var meta map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&meta); err != nil || meta == nil {
		return nil, fmt.Errorf("interface metadata must be an object")
	}
	return meta, nil
}

func interfaceCapacityReadData(kind, raw, ownerRaw string) (string, []string) {
	var owner map[string]any
	_ = json.Unmarshal([]byte(ownerRaw), &owner)
	meta, decodeErr := decodeInterfaceMetadata(raw)
	issues := []string{}
	if decodeErr != nil {
		meta = map[string]any{}
		issues = append(issues, decodeErr.Error())
	}
	if err := normalizeInterfaceVRFs(owner, meta); err != nil {
		issues = append(issues, err.Error())
	}
	capacityIssues, err := normalizeInterfaceCapacity(kind, owner, meta, false)
	if err != nil {
		issues = append(issues, err.Error())
	} else {
		issues = append(issues, capacityIssues...)
	}
	meta["effective_vrfs"] = effectiveInterfaceVRFs(owner, meta)
	meta["validation_issues"] = issues
	if budgets, err := interfaceBudgetMap(meta["vrf_bandwidth_bps"]); err == nil {
		var total int64
		for _, rate := range budgets {
			if total > maxInterfaceRate-rate {
				total = maxInterfaceRate
				break
			}
			total += rate
		}
		meta["allocated_bps"] = total
		if speed, ok := meta["speed_bps"].(int64); ok {
			meta["remaining_bps"] = speed - total
		}
	}
	encoded, _ := json.Marshal(meta)
	return string(encoded), issues
}

func (a *App) listInterfaces(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	limit := 2000
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > 5000 {
		limit = 5000
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	query := `SELECT i.id,i.device_id,i.name,i.data,o.kind,o.data FROM interfaces i JOIN objects o ON o.id=i.device_id`
	args := []any{}
	if list := r.URL.Query().Get("ids"); list != "" {
		ids := strings.Split(list, ",")
		if len(ids) > 500 {
			http.Error(w, "request at most 500 interface IDs", 400)
			return
		}
		query += ` WHERE i.id IN (` + strings.TrimRight(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	} else if device := r.URL.Query().Get("device_id"); device != "" {
		query += ` WHERE i.device_id=?`
		args = append(args, device)
	} else if list := r.URL.Query().Get("device_ids"); list != "" {
		ids := strings.Split(list, ",")
		if len(ids) > 500 {
			http.Error(w, "request at most 500 device IDs", 400)
			return
		}
		query += ` WHERE i.device_id IN (` + strings.TrimRight(strings.Repeat("?,", len(ids)), ",") + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += ` ORDER BY i.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.Query(query, args...)
	if err != nil {
		http.Error(w, "interface lookup failed", 500)
		return
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, device, name, raw, kind, ownerRaw string
		if err = rows.Scan(&id, &device, &name, &raw, &kind, &ownerRaw); err != nil {
			http.Error(w, "interface read failed", 500)
			return
		}
		data, _ := interfaceCapacityReadData(kind, raw, ownerRaw)
		out = append(out, map[string]string{"id": id, "device_id": device, "name": name, "data": data, "revision": interfaceRevision(id, device, name, raw, ownerRaw)})
	}
	if rows.Err() != nil {
		http.Error(w, "interface read failed", 500)
		return
	}
	json.NewEncoder(w).Encode(out)
}

type interfaceBudgetChange struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func pruneInterfaceBudgets(owner, port map[string]any) bool {
	budgets, err := interfaceBudgetMap(port["vrf_bandwidth_bps"])
	if err != nil || len(budgets) == 0 {
		return false
	}
	carried := map[string]bool{}
	for _, name := range effectiveInterfaceVRFs(owner, port) {
		carried[name] = true
	}
	changed := false
	for name := range budgets {
		if !carried[name] {
			delete(budgets, name)
			changed = true
		}
	}
	if changed {
		port["vrf_bandwidth_bps"] = budgets
	}
	return changed
}

func reconcileInterfaceBudgets(tx *sql.Tx, id string, owner map[string]any) ([]interfaceBudgetChange, error) {
	rows, err := tx.Query(`SELECT id,data FROM interfaces WHERE device_id=?`, id)
	if err != nil {
		return nil, err
	}
	changes := []interfaceBudgetChange{}
	for rows.Next() {
		var port, raw string
		if err = rows.Scan(&port, &raw); err != nil {
			break
		}
		meta, _ := decodeInterfaceMetadata(raw)
		if pruneInterfaceBudgets(owner, meta) {
			encoded, _ := json.Marshal(meta)
			changes = append(changes, interfaceBudgetChange{port, raw, string(encoded)})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	for _, change := range changes {
		if _, err = tx.Exec(`UPDATE interfaces SET data=? WHERE id=? AND device_id=?`, change.After, change.ID, id); err != nil {
			return nil, err
		}
	}
	return changes, nil
}
