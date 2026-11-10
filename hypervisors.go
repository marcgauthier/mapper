package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func isHypervisorMeta(meta map[string]any) bool {
	visual, _ := meta["device_visual"].(string)
	if visual == "esxi" || visual == "hypervisor" {
		return true
	}
	// Preserve legacy free-form ESXi/VMware inventory and recognize common platforms.
	for _, key := range []string{"device_type_id", "device_type", "type"} {
		value, _ := meta[key].(string)
		text := " " + strings.NewReplacer("-", " ", "_", " ", "/", " ", "(", " ", ")", " ").Replace(strings.ToLower(value)) + " "
		for _, name := range []string{"esxi", "vmware", "vsphere", "hypervisor", "hyper v", "hyperv", "proxmox", "kvm", "xen", "xcp ng", "xcpng", "nutanix", "ahv", "ovirt", "bhyve"} {
			if strings.Contains(text, " "+name+" ") {
				return true
			}
		}
	}
	return false
}

func hostedKind(kind string) bool { return kind == "virtual_machine" || kind == "virtual_switch" }

// sidedDeviceInterfaces names the role interfaces created with special device
// types. Each side keeps its role in interface data for display and to allow
// future connection rules per side.
var sidedDeviceInterfaces = map[string][]string{
	"inline-encryptor": {"red", "black"},
	"serial-encryptor": {"red", "black"},
	"airgap":           {"low", "high"},
}

// API edits and CSV import share the same parent and catalog validation.
func prepareHostedObject(tx *sql.Tx, id, kind string, meta map[string]any) error {
	hostID, _ := meta["host_id"].(string)
	var hostKind, raw string
	if hostID == "" || hostID == id || tx.QueryRow(`SELECT kind,data FROM objects WHERE id=?`, hostID).Scan(&hostKind, &raw) != nil || hostKind != "device" {
		return fmt.Errorf("host_id must reference an existing hypervisor device")
	}
	var host map[string]any
	if json.Unmarshal([]byte(raw), &host) != nil || !isHypervisorMeta(host) {
		return fmt.Errorf("parent device must be a hypervisor")
	}
	typeID, _ := meta["device_type_id"].(string)
	var typeKind, typeName, visual string
	if typeID == "" || tx.QueryRow(`SELECT object_kind,name,visual FROM device_types WHERE id=? AND enabled=1`, typeID).Scan(&typeKind, &typeName, &visual) != nil || typeKind != kind {
		return fmt.Errorf("select a catalog type matching the hosted item category")
	}
	meta["device_type"], meta["device_visual"] = typeName, visual
	if kind == "virtual_machine" {
		guestOS, _ := meta["guest_os"].(string)
		if strings.EqualFold(guestOS, "linux") {
			meta["guest_os"] = "Linux"
		} else if strings.EqualFold(guestOS, "windows") {
			meta["guest_os"] = "Windows"
		} else {
			return fmt.Errorf("virtual machine guest_os must be Linux or Windows")
		}
	} else {
		delete(meta, "guest_os")
	}
	for _, key := range []string{"site_id", "building_id", "floor_id", "room_id", "telecom_room_id", "rack_id"} {
		if value, exists := host[key]; exists {
			meta[key] = value
		} else {
			delete(meta, key)
		}
	}
	for _, key := range []string{"network", "supportgroup"} {
		if meta[key] == nil || meta[key] == "" {
			if value, exists := host[key]; exists {
				meta[key] = value
			}
		}
	}
	return nil
}
