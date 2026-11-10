package app

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// VRFs are local routing domains, independent of the managed network labels.
// An omitted router assignment means all device VRFs, including future additions.
func vrfRole(meta map[string]any) string {
	visual := topologyText(meta, "device_visual")
	if visual == "" {
		visual = topologyText(meta, "device_type_id")
	}
	if visual == "" {
		visual = topologyText(meta, "device_type")
	}
	visual = strings.ToLower(visual)
	if strings.Contains(visual, "router") {
		return "router"
	}
	if strings.Contains(visual, "switch") {
		return "switch"
	}
	return ""
}

func vrfNames(value any) ([]string, error) {
	out := []string{}
	if value == nil {
		return out, nil
	}
	var values []any
	switch v := value.(type) {
	case []any:
		values = v
	case []string:
		for _, s := range v {
			values = append(values, s)
		}
	default:
		return nil, fmt.Errorf("VRFs must be an array of names")
	}
	if len(values) > 256 {
		return nil, fmt.Errorf("a device may declare at most 256 VRFs")
	}
	seen := map[string]bool{}
	for _, value := range values {
		name, ok := value.(string)
		name = strings.TrimSpace(name)
		if !ok || name == "" || utf8.RuneCountInString(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("each VRF requires a name of 1–128 characters without control characters")
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate VRF %q", name)
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

func normalizeInterfaceVRFs(owner, meta map[string]any) error {
	delete(meta, "effective_vrfs") // Computed on reads; never accept it as inventory.
	names, err := vrfNames(meta["vrfs"])
	if err != nil {
		return err
	}
	mode, ok := meta["vrf_mode"].(string)
	if meta["vrf_mode"] != nil && !ok {
		return fmt.Errorf("vrf_mode must be all or selected")
	}
	role := vrfRole(owner)
	if mode == "" {
		_, explicit := meta["vrfs"]
		if role == "router" && !explicit {
			mode = "all"
		} else {
			mode = "selected"
		}
	}
	if mode != "all" && mode != "selected" {
		return fmt.Errorf("vrf_mode must be all or selected")
	}
	if mode == "all" && role != "router" {
		return fmt.Errorf("only router interfaces can use all device VRFs")
	}
	if role != "router" && role != "switch" && len(names) > 0 {
		return fmt.Errorf("VRFs require a switch or router")
	}
	if role == "switch" && len(names) > 1 {
		return fmt.Errorf("a switch interface can select only one VRF")
	}
	available, err := vrfNames(owner["vrfs"])
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, name := range available {
		known[name] = true
	}
	if mode == "all" {
		names = []string{}
	} else {
		for _, name := range names {
			if !known[name] {
				return fmt.Errorf("VRF %q is not declared on this device", name)
			}
		}
	}
	if role != "" || meta["vrfs"] != nil || meta["vrf_mode"] != nil {
		meta["vrf_mode"], meta["vrfs"] = mode, names
	}
	return nil
}

// Validate edits against this device's indexed interfaces without scanning inventory.
func normalizeDeviceVRFs(tx *sql.Tx, id, kind string, meta map[string]any) error {
	names, err := vrfNames(meta["vrfs"])
	if err != nil {
		return err
	}
	if len(names) > 0 && ((kind != "device" && kind != "virtual_switch") || vrfRole(meta) == "") {
		return fmt.Errorf("VRFs can be declared on switches and routers")
	}
	if _, exists := meta["vrfs"]; exists {
		meta["vrfs"] = names
	}
	rows, err := tx.Query(`SELECT name,data FROM interfaces WHERE device_id=?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, raw string
		if err = rows.Scan(&name, &raw); err != nil {
			return err
		}
		port, decodeErr := decodeInterfaceMetadata(raw)
		if decodeErr != nil {
			continue
		}
		pruneInterfaceBudgets(meta, port)
		if err = normalizeInterfaceVRFs(meta, port); err != nil {
			return fmt.Errorf("interface %s: %w; update its VRF assignment first", name, err)
		}
		if _, err = normalizeInterfaceCapacity(kind, meta, port, false); err != nil {
			// Unknown legacy speeds do not block unrelated device edits.
			if topologyText(port, "speed") != "" {
				if _, speedErr := parseNetworkRate(topologyText(port, "speed")); speedErr != nil {
					delete(port, "speed")
					_, err = normalizeInterfaceCapacity(kind, meta, port, false)
				}
			}
			if err != nil {
				return fmt.Errorf("interface %s: %w", name, err)
			}
		}
	}
	return rows.Err()
}
