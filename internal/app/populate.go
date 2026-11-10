package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
)

// populateOptions configures POST /api/populate. Zero numbers select defaults
// and nil toggles enable the feature; explicit out-of-range values are rejected.
type populateOptions struct {
	Sites        int   `json:"sites"`
	Buildings    int   `json:"buildings_per_site"`
	Rooms        int   `json:"rooms_per_building"`
	TelecomRooms int   `json:"telecom_rooms_per_building"`
	DevicesMin   int   `json:"devices_min_per_room"`
	DevicesMax   int   `json:"devices_max_per_room"`
	Seed         int64 `json:"seed"`
	Racks        *bool `json:"racks"`
	Conduits     *bool `json:"conduits"`
	Power        *bool `json:"power"`
	Transports   *bool `json:"transports"`
	Cloud        *bool `json:"cloud"`
}

type populateConfig struct {
	sites, buildings, rooms, telecoms, devMin, devMax int
	seed                                              int64
	racks, conduits, power, transports, cloud         bool
}

func populateFlag(value *bool) bool { return value == nil || *value }

func (o populateOptions) resolve() (populateConfig, error) {
	c := populateConfig{
		sites: 50, buildings: 10, rooms: 5, telecoms: 2, devMin: 5, devMax: 10,
		seed: 1, racks: true, conduits: true, power: true, transports: true, cloud: true,
	}
	if o.Sites != 0 {
		c.sites = o.Sites
	}
	if o.Buildings != 0 {
		c.buildings = o.Buildings
	}
	if o.Rooms != 0 {
		c.rooms = o.Rooms
	}
	if o.TelecomRooms != 0 {
		c.telecoms = o.TelecomRooms
	}
	if o.DevicesMin != 0 || o.DevicesMax != 0 {
		c.devMin, c.devMax = o.DevicesMin, o.DevicesMax
	}
	if o.Seed != 0 {
		c.seed = o.Seed
	}
	c.racks, c.conduits, c.power, c.transports, c.cloud =
		populateFlag(o.Racks), populateFlag(o.Conduits), populateFlag(o.Power), populateFlag(o.Transports), populateFlag(o.Cloud)
	if c.sites < 1 || c.sites > 200 {
		return c, fmt.Errorf("sites must be between 1 and 200")
	}
	if c.buildings < 1 || c.buildings > 20 {
		return c, fmt.Errorf("buildings_per_site must be between 1 and 20")
	}
	if c.rooms < 1 || c.rooms > 12 {
		return c, fmt.Errorf("rooms_per_building must be between 1 and 12")
	}
	if c.telecoms < 1 || c.telecoms > c.rooms {
		return c, fmt.Errorf("telecom_rooms_per_building must be between 1 and rooms_per_building")
	}
	if c.devMin < 0 || c.devMax < 0 || c.devMax > 24 || c.devMin > c.devMax {
		return c, fmt.Errorf("device counts must satisfy 0 <= min <= max <= 24")
	}
	return c, nil
}

var populateCities = []string{"Austin", "Berlin", "Toronto", "Oslo", "Denver", "Lyon", "Seoul", "Madrid", "Boston", "Perth", "Lisbon", "Miami", "Osaka", "Dublin", "Calgary", "Nice", "Chicago", "Bergen", "Sydney", "Porto", "Atlanta", "Lille", "Nagoya", "Valencia", "Halifax", "Toulouse", "Busan", "Bilbao", "Phoenix", "Nantes", "Sapporo", "Granada", "Vancouver", "Rennes", "Daegu", "Malaga", "Dallas", "Linz", "Kyoto", "Seville", "Ottawa", "Brest", "Incheon", "Zaragoza", "Tampa", "Dijon", "Kobe", "Murcia", "Edmonton", "Caen", "Fukuoka", "Vigo", "Orlando", "Reims", "Hiroshima", "Alicante", "Winnipeg", "Metz", "Sendai", "Cordoba", "Tucson", "Clermont", "Niigata", "Gijon"}

var populateSiteRoles = []string{"major_site", "branch_office", "edge_site", "branch_office", "cloud_colo", "branch_office", "disaster_recovery"}

var populateVRFs = []string{"Management", "Production", "Guest", "Voice"}

func (a *App) populate(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if p.Role == "viewer" {
		http.Error(w, "editor required", 403)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var opts populateOptions
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil && err != io.EOF {
			http.Error(w, "invalid options", 400)
			return
		}
	}
	cfg, err := opts.resolve()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer a.rollbackLogged(tx)
	// Replace semantics: clear previous demo data first so re-running with
	// different options never orphans rows or collides roles on reused ids.
	for _, q := range []string{
		`DELETE FROM cable_routes WHERE id LIKE 'pop:%'`,
		`DELETE FROM patch_pairings WHERE id LIKE 'pop:%'`,
		`DELETE FROM links WHERE id LIKE 'pop:%'`,
		`DELETE FROM interfaces WHERE id LIKE 'pop:%'`,
		`DELETE FROM relationships WHERE id LIKE 'pop:%'`,
		`DELETE FROM layouts WHERE object_id LIKE 'pop:%' OR object_id LIKE 'ground:pop:%'`,
		`DELETE FROM objects WHERE id LIKE 'pop:%' OR id LIKE 'ground:pop:%'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			http.Error(w, "demo data reset failed", 500)
			return
		}
	}
	g := &populator{
		tx: tx, rnd: rand.New(rand.NewSource(cfg.seed)), cfg: cfg,
		counts: map[string]int{}, kinds: map[string]string{}, metas: map[string]map[string]any{},
		roles: map[string]string{}, used: map[string]bool{}, ports: map[string]int{},
		coords: map[string][2]float64{}, roomByID: map[string]*popRoom{},
		catalog: map[string]popDeviceType{}, rr: map[string]int{},
	}
	if err := g.loadCatalog(); err != nil {
		http.Error(w, "device catalog unavailable", 500)
		return
	}
	if err := g.generate(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	summary := map[string]any{"options": map[string]any{
		"sites": cfg.sites, "buildings_per_site": cfg.buildings, "rooms_per_building": cfg.rooms,
		"telecom_rooms_per_building": cfg.telecoms, "devices_min_per_room": cfg.devMin,
		"devices_max_per_room": cfg.devMax, "seed": cfg.seed, "racks": cfg.racks,
		"conduits": cfg.conduits, "power": cfg.power, "transports": cfg.transports, "cloud": cfg.cloud,
	}, "counts": g.counts}
	if err := a.recordAudit(tx, p.User, "populate", "demo", summary); err != nil {
		http.Error(w, "audit write failed", 500)
		return
	}
	event, _ := json.Marshal(map[string]any{"type": "populate", "counts": g.counts})
	seq, err := appendEvent(tx, event)
	if err != nil {
		http.Error(w, "event write failed", 500)
		return
	}
	if err := a.commitLogged(tx); err != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	a.notify(seq, event)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

type popDeviceType struct {
	id, name, kind, visual, medium string
}

// populator generates deterministic demo inventory. Every id derives from the
// structural path, so re-running with the same options upserts the same rows
// instead of duplicating them.
type populator struct {
	tx  *sql.Tx
	rnd *rand.Rand
	cfg populateConfig

	counts   map[string]int
	kinds    map[string]string
	metas    map[string]map[string]any
	roles    map[string]string
	used     map[string]bool
	ports    map[string]int
	coords   map[string][2]float64
	roomByID map[string]*popRoom

	linkSeq, relSeq, routeSeq, pairSeq, svcPortSeq, circuitSeq int

	catalog                            map[string]popDeviceType
	routers, switches, firewalls       []popDeviceType
	hypervisors, servers, endpoints    []popDeviceType
	vms, vswitches, panelsCu, panelsFb []popDeviceType
	rr                                 map[string]int
}

func (g *populator) loadCatalog() error {
	rows, err := g.tx.Query(`SELECT id,name,object_kind,visual,medium FROM device_types WHERE enabled=1 ORDER BY sort_order,name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t popDeviceType
		if err := rows.Scan(&t.id, &t.name, &t.kind, &t.visual, &t.medium); err != nil {
			return err
		}
		g.catalog[t.id] = t
		switch t.kind {
		case "virtual_machine":
			g.vms = append(g.vms, t)
		case "virtual_switch":
			g.vswitches = append(g.vswitches, t)
		case "patch_panel":
			if t.medium == "fiber" {
				g.panelsFb = append(g.panelsFb, t)
			} else {
				g.panelsCu = append(g.panelsCu, t)
			}
		case "device":
			meta := map[string]any{"device_type_id": t.id, "device_type": t.name, "device_visual": t.visual}
			text := strings.ToLower(t.id + " " + t.name + " " + t.visual)
			switch {
			case isHypervisorMeta(meta):
				g.hypervisors = append(g.hypervisors, t)
			case strings.Contains(text, "router"):
				g.routers = append(g.routers, t)
			case strings.Contains(text, "switch"):
				g.switches = append(g.switches, t)
			case strings.Contains(text, "firewall"):
				g.firewalls = append(g.firewalls, t)
			case strings.Contains(text, "server") || strings.Contains(text, "storage") || strings.Contains(text, "mainframe"):
				g.servers = append(g.servers, t)
			default:
				if _, sided := sidedDeviceInterfaces[t.id]; sided {
					continue
				}
				switch t.visual {
				case "cloud", "saas", "paas", "iaas", "faas", "container", "pod":
					continue
				}
				g.endpoints = append(g.endpoints, t)
			}
		}
	}
	return rows.Err()
}

func (g *populator) pick(bucket []popDeviceType, key string) (popDeviceType, bool) {
	if len(bucket) == 0 {
		return popDeviceType{}, false
	}
	t := bucket[g.rr[key]%len(bucket)]
	g.rr[key]++
	return t, true
}

// applyType assigns a catalog type when available, else records the fallback
// free-form type so VRF and hypervisor detection keep working.
func (g *populator) applyType(meta map[string]any, t popDeviceType, ok bool, fallback, visual string) {
	if ok {
		meta["device_type_id"] = t.id
		return
	}
	meta["device_type"], meta["device_visual"] = fallback, visual
}

// addObject runs a new object through the same normalization chain as POST
// /api/objects, then upserts it by its deterministic id.
func (g *populator) addObject(kind, id, name string, x, y float64, meta map[string]any) error {
	v := map[string]any{}
	for k, val := range meta {
		v[k] = val
	}
	v["id"], v["kind"], v["name"], v["x"], v["y"] = id, kind, name, x, y
	if kind == "site" && !siteRoles[topologyText(v, "site_role")] {
		return fmt.Errorf("populate %s: missing site role", id)
	}
	if err := normalizeTopologyPlacement(g.tx, id, kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if err := normalizeLocationParent(g.tx, kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if err := normalizeRackPlacement(g.tx, kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if err := normalizeConduitEndpoints(g.tx, kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if err := requirePlacement(kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if typeID, _ := v["device_type_id"].(string); typeID != "" {
		t, found := g.catalog[typeID]
		if !found || t.kind != kind {
			return fmt.Errorf("populate %s: device type %s unavailable", id, typeID)
		}
		v["device_type"], v["device_visual"], v["medium"] = t.name, t.visual, t.medium
	} else if hostedKind(kind) {
		return fmt.Errorf("populate %s: hosted item requires a catalog device type", id)
	}
	if err := normalizeDeviceVRFs(g.tx, id, kind, v); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if hostedKind(kind) {
		if err := prepareHostedObject(g.tx, id, kind, v); err != nil {
			return fmt.Errorf("populate %s: %w", id, err)
		}
	}
	data, _ := json.Marshal(v)
	if _, err := g.tx.Exec(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,name=excluded.name,data=excluded.data,x=excluded.x,y=excluded.y,version=objects.version+1`, id, kind, name, string(data), x, y); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if kind == "building" {
		if err := ensureGround(g.tx, id); err != nil {
			return fmt.Errorf("populate %s: %w", id, err)
		}
	}
	g.counts[kind]++
	g.kinds[id] = kind
	g.metas[id] = v
	g.coords[id] = [2]float64{x, y}
	return nil
}

func (g *populator) addInterface(id, deviceID, name string, meta map[string]any) error {
	owner, ok := g.metas[deviceID]
	if !ok {
		return fmt.Errorf("populate %s: unknown owner %s", id, deviceID)
	}
	if err := normalizeInterfaceVRFs(owner, meta); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	if _, err := normalizeInterfaceCapacity(g.kinds[deviceID], owner, meta, true); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	b, _ := json.Marshal(meta)
	if _, err := g.tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,data=excluded.data`, id, deviceID, name, string(b)); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	g.counts["interfaces"]++
	return nil
}

// port allocates the next numbered interface on a device.
func (g *populator) port(deviceID, prefix string, meta map[string]any) (string, error) {
	n := g.ports[deviceID]
	g.ports[deviceID] = n + 1
	id := fmt.Sprintf("pop:p:%s:%d", strings.TrimPrefix(deviceID, "pop:"), n)
	if err := g.addInterface(id, deviceID, fmt.Sprintf("%s%d", prefix, n), meta); err != nil {
		return "", err
	}
	return id, nil
}

func (g *populator) addLink(a, b string, spec map[string]any) (string, error) {
	if a == "" || b == "" {
		return "", fmt.Errorf("populate: link requires two interfaces")
	}
	if g.used[a] || g.used[b] {
		return "", fmt.Errorf("populate: interface %s reused", a)
	}
	clean, err := validateLinkSpec(spec)
	if err != nil {
		return "", fmt.Errorf("populate link %s-%s: %w", a, b, err)
	}
	g.linkSeq++
	id := fmt.Sprintf("pop:link:%06d", g.linkSeq)
	data, _ := json.Marshal(clean)
	if _, err := g.tx.Exec(`INSERT INTO links(id,source_interface,target_interface,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_interface=excluded.source_interface,target_interface=excluded.target_interface,data=excluded.data`, id, a, b, string(data)); err != nil {
		return "", fmt.Errorf("populate %s: %w", id, err)
	}
	g.used[a], g.used[b] = true, true
	g.counts["links"]++
	return id, nil
}

func (g *populator) addRelationship(source, target, typ, label string) error {
	g.relSeq++
	rel := topologyRelationship{ID: fmt.Sprintf("pop:rel:%05d", g.relSeq), Source: source, Target: target, Type: typ, Version: 1, Label: label}
	if err := validateRelationship(g.tx, rel); err != nil {
		return fmt.Errorf("populate %s: %w", rel.ID, err)
	}
	if _, err := g.tx.Exec(`INSERT INTO relationships(id,source_id,target_id,relationship_type,version,label) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET source_id=excluded.source_id,target_id=excluded.target_id,relationship_type=excluded.relationship_type,label=excluded.label`, rel.ID, source, target, typ, 1, label); err != nil {
		return fmt.Errorf("populate %s: %w", rel.ID, err)
	}
	g.counts["relationships"]++
	return nil
}

func (g *populator) addCableRoute(linkID, conduitID string, sequence int) error {
	id := fmt.Sprintf("pop:cr:%s:%d", strings.TrimPrefix(linkID, "pop:link:"), sequence)
	if _, err := g.tx.Exec(`INSERT INTO cable_routes(id,link_id,sequence,infrastructure_id) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET link_id=excluded.link_id,sequence=excluded.sequence,infrastructure_id=excluded.infrastructure_id`, id, linkID, sequence, conduitID); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	g.counts["cable_routes"]++
	return nil
}

func (g *populator) addPairing(panel, a, b string) error {
	g.pairSeq++
	id := fmt.Sprintf("pop:pair:%06d", g.pairSeq)
	if _, err := g.tx.Exec(`INSERT INTO patch_pairings(id,panel_id,port_a,port_b) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET panel_id=excluded.panel_id,port_a=excluded.port_a,port_b=excluded.port_b`, id, panel, a, b); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	g.counts["pairings"]++
	return nil
}

func (g *populator) ensureNetwork(id, name, description string, order int) error {
	if _, err := g.tx.Exec(`INSERT OR IGNORE INTO networks(id,name,description,sort_order,enabled) VALUES(?,?,?,?,1)`, id, name, description, order); err != nil {
		return fmt.Errorf("populate %s: %w", id, err)
	}
	g.counts["networks"]++
	return nil
}

// attachService mirrors POST /api/topology/attachment: a provider-service link
// between a gateway device interface and a fresh service-side port.
func (g *populator) attachService(gateway, service, circuit, bandwidth, label string) error {
	var raw string
	if err := g.tx.QueryRow(`SELECT data FROM objects WHERE id=? AND kind='service'`, service).Scan(&raw); err != nil {
		return fmt.Errorf("populate: service %s missing", service)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(raw), &m)
	category := topologyText(m, "service_category")
	g.svcPortSeq++
	port := fmt.Sprintf("pop:sp:%06d", g.svcPortSeq)
	b, _ := json.Marshal(map[string]any{})
	if _, err := g.tx.Exec(`INSERT INTO interfaces(id,device_id,name,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,data=excluded.data`, port, service, "Attachment "+fmt.Sprint(g.svcPortSeq), string(b)); err != nil {
		return fmt.Errorf("populate %s: %w", port, err)
	}
	g.counts["interfaces"]++
	spec := map[string]any{"connection_class": "provider_service", "service_type": category, "circuit_id": circuit, "bandwidth": bandwidth, "label": label, "provider": topologyText(m, "provider")}
	if err := validateServiceLink(g.tx, gateway, port, spec); err != nil {
		return fmt.Errorf("populate attachment: %w", err)
	}
	if _, err := g.addLink(gateway, port, spec); err != nil {
		return err
	}
	return nil
}

type popRoom struct {
	id, name, tag   string
	floor           string
	ground, telecom bool
	tidx, slot      int
	devices         []string
	vrf             string
	outlets         []string
}

type popBldg struct {
	site, bldg           int
	siteID, bldgID       string
	ground, l1           string
	letter               string
	core                 string
	rooms                []*popRoom
	telecoms             map[int]*popRoom
	switches             map[int][]string
	panelCu, panelFb     string
	router, edgeFw       string
	hypervisors          []string
	vswitchOf            map[string]string
	vmsOf                map[string][]string
	riserGround, riserL1 string
	groundCount, l1Count int
	rackRR               map[string]int
}

func (g *populator) generate() error {
	if err := g.ensureNetwork("pop:net:prod", "Production", "Populated production network", 10); err != nil {
		return err
	}
	if err := g.ensureNetwork("pop:net:lab", "Lab", "Populated lab network", 20); err != nil {
		return err
	}
	if err := g.ensureNetwork("pop:net:ot", "OT", "Populated operational technology network", 30); err != nil {
		return err
	}
	if g.cfg.transports {
		transports := []struct{ id, name, category, provider string }{
			{"pop:svc:internet", "Internet", "internet", "Carrier"},
			{"pop:svc:mpls", "MPLS", "mpls", "Carrier"},
			{"pop:svc:pls", "PLS", "pls", "Carrier"},
		}
		for i, svc := range transports {
			if err := g.addObject("service", svc.id, svc.name, float64(i*420), -320, map[string]any{
				"service_category": svc.category, "provider": svc.provider,
				"description": "Populated " + svc.name + " transport cloud",
			}); err != nil {
				return err
			}
		}
	}
	if g.cfg.cloud {
		cloud := []struct{ id, name, category string }{
			{"pop:svc:aws", "AWS", "cloud_provider"},
			{"pop:svc:app", "Hosted app", "paas"},
			{"pop:svc:db", "Shared DB", "iaas"},
			{"pop:svc:email", "Email", "saas"},
		}
		for i, svc := range cloud {
			if err := g.addObject("service", svc.id, svc.name, float64(i*420), -640, map[string]any{
				"service_category": svc.category, "provider": "AWS",
				"description": "Populated " + svc.name,
			}); err != nil {
				return err
			}
		}
		if err := g.addRelationship("pop:svc:aws", "pop:svc:app", "hosts", ""); err != nil {
			return err
		}
		if err := g.addRelationship("pop:svc:aws", "pop:svc:db", "hosts", ""); err != nil {
			return err
		}
		if err := g.addRelationship("pop:svc:app", "pop:svc:db", "depends_on", "reads and writes"); err != nil {
			return err
		}
	}
	cores := map[int]string{}
	for s := 1; s <= g.cfg.sites; s++ {
		core, err := g.addSite(s)
		if err != nil {
			return err
		}
		cores[s] = core
	}
	if g.cfg.cloud {
		for s := 1; s <= g.cfg.sites; s++ {
			if cores[s] == "" {
				continue
			}
			if err := g.addRelationship(cores[s], "pop:svc:email", "accesses", "site mail flow"); err != nil {
				return err
			}
		}
	}
	if g.cfg.transports && g.cfg.sites > 1 && cores[1] != "" {
		for s := 2; s <= g.cfg.sites; s++ {
			if cores[s] == "" {
				continue
			}
			if err := g.addTunnel(s, cores[1], cores[s]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *populator) addSite(s int) (string, error) {
	role := "data_center"
	if s > 1 {
		role = populateSiteRoles[(s-2)%len(populateSiteRoles)]
	}
	cityKey := int(g.cfg.seed) + s - 1
	name := populateCities[cityKey%len(populateCities)]
	if cycle := cityKey / len(populateCities); cycle > 0 {
		name += fmt.Sprintf(" %d", cycle+1)
	}
	siteID := fmt.Sprintf("pop:s%02d", s)
	if err := g.addObject("site", siteID, name, float64((s-1)%10*420), float64((s-1)/10*420), map[string]any{
		"site_role": role, "network": []any{"pop:net:prod"},
	}); err != nil {
		return "", err
	}
	core, err := g.addBuilding(s, 1, siteID, role, "")
	if err != nil {
		return "", err
	}
	for b := 2; b <= g.cfg.buildings; b++ {
		if _, err := g.addBuilding(s, b, siteID, role, core); err != nil {
			return "", err
		}
	}
	if g.cfg.conduits {
		if err := g.addSiteConduits(s, siteID); err != nil {
			return "", err
		}
	}
	return core, nil
}

func (g *populator) addBuilding(s, b int, siteID, siteRole, core string) (string, error) {
	letter := string(rune('A' + b - 1))
	bldgID := fmt.Sprintf("%s:b%02d", siteID, b)
	if err := g.addObject("building", bldgID, "Building "+letter, float64((b-1)%5*320), float64((b-1)/5*320), map[string]any{
		"site_id": siteID, "network": []any{"pop:net:prod"},
	}); err != nil {
		return "", err
	}
	ground := groundID(bldgID)
	l1 := fmt.Sprintf("%s:f1", bldgID)
	if err := g.addObject("floor", l1, "Level 1", 0, 240, map[string]any{"building_id": bldgID}); err != nil {
		return "", err
	}
	if g.cfg.power {
		if err := g.addObject("ups", bldgID+":u", "UPS-"+letter, -260, 0, map[string]any{"building_id": bldgID}); err != nil {
			return "", err
		}
		if siteRole == "data_center" || b%3 == 1 {
			if err := g.addObject("generator", bldgID+":g", "GEN-"+letter, -260, 140, map[string]any{"building_id": bldgID}); err != nil {
				return "", err
			}
		}
	}
	ctx := &popBldg{
		site: s, bldg: b, siteID: siteID, bldgID: bldgID, ground: ground, l1: l1,
		letter: letter, core: core, telecoms: map[int]*popRoom{}, switches: map[int][]string{},
		vswitchOf: map[string]string{}, vmsOf: map[string][]string{}, rackRR: map[string]int{},
	}
	for i := 1; i <= g.cfg.rooms; i++ {
		floor, rowGround := ground, true
		if i%2 == 0 {
			floor, rowGround = l1, false
		}
		if i > g.cfg.rooms-g.cfg.telecoms {
			if err := g.addTelecomRoom(ctx, i, i-(g.cfg.rooms-g.cfg.telecoms), floor, rowGround); err != nil {
				return "", err
			}
			continue
		}
		if err := g.addOrdRoom(ctx, i, floor, rowGround); err != nil {
			return "", err
		}
	}
	if err := g.cableBuilding(ctx); err != nil {
		return "", err
	}
	return ctx.router, nil
}

func (g *populator) roomXY(ctx *popBldg, rowGround bool) (float64, float64) {
	if rowGround {
		x := float64(ctx.groundCount * 260)
		ctx.groundCount++
		return x, 0
	}
	x := float64(ctx.l1Count * 260)
	ctx.l1Count++
	return x, 240
}

func (g *populator) addTelecomRoom(ctx *popBldg, slot, tidx int, floor string, rowGround bool) error {
	id := fmt.Sprintf("%s:t%d", ctx.bldgID, tidx)
	name, tag := "MDF", "MDF"
	if tidx > 1 {
		name, tag = fmt.Sprintf("IDF-%d", tidx-1), fmt.Sprintf("IDF%d", tidx-1)
	}
	x, y := g.roomXY(ctx, rowGround)
	if err := g.addObject("telecom_room", id, name, x, y, map[string]any{"floor_id": floor}); err != nil {
		return err
	}
	room := &popRoom{id: id, name: name, tag: tag, floor: floor, ground: rowGround, telecom: true, tidx: tidx, slot: slot}
	ctx.rooms = append(ctx.rooms, room)
	ctx.telecoms[tidx] = room
	g.roomByID[id] = room
	if rowGround && ctx.riserGround == "" {
		ctx.riserGround = id
	}
	if !rowGround && ctx.riserL1 == "" {
		ctx.riserL1 = id
	}
	var racks []string
	if g.cfg.racks {
		for k, label := range []string{"Rack A", "Rack B"} {
			rackID := fmt.Sprintf("%s:k%d", id, k+1)
			if err := g.addObject("rack", rackID, label, -240, float64(k*150), map[string]any{"telecom_room_id": id}); err != nil {
				return err
			}
			racks = append(racks, rackID)
		}
	}
	if g.cfg.power {
		if err := g.addObject("ups", id+":u", fmt.Sprintf("UPS-%s-%s", ctx.letter, tag), -240, 300, map[string]any{"telecom_room_id": id}); err != nil {
			return err
		}
	}
	if tidx == 1 {
		panelMeta := func() map[string]any {
			if len(racks) > 0 {
				return map[string]any{"rack_id": racks[0]}
			}
			return map[string]any{"telecom_room_id": id}
		}
		{
			pm := panelMeta()
			if t, ok := g.pick(g.panelsCu, "panelCu"); ok {
				pm["device_type_id"] = t.id
			}
			ctx.panelCu, _ = g.addPanel(id+":pc", "PP-CU-1", pm, -240, 150)
		}
		if t, ok := g.pick(g.panelsFb, "panelFb"); ok {
			fm := panelMeta()
			fm["device_type_id"] = t.id
			ctx.panelFb, _ = g.addPanel(id+":pf", "PP-FB-1", fm, -240, 220)
		} else {
			ctx.panelFb, _ = g.addPanel(id+":pf", "PP-FB-1", panelMeta(), -240, 220)
		}
		if ctx.panelCu == "" || ctx.panelFb == "" {
			return fmt.Errorf("populate %s: panel creation failed", id)
		}
	}
	n := g.cfg.devMin
	if g.cfg.devMax > g.cfg.devMin {
		n += g.rnd.Intn(g.cfg.devMax - g.cfg.devMin + 1)
	}
	coreRoom := ctx.bldg == 1 && tidx == 1
	for d := 1; d <= n; d++ {
		role := g.telecomMix()
		switch d {
		case 1:
			role = "switch"
		case 2:
			if coreRoom || tidx == 1 {
				role = "router"
			}
		case 3:
			if coreRoom {
				role = "firewall-edge"
			}
		}
		devID, err := g.addDevice(ctx, room, d, role)
		if err != nil {
			return err
		}
		room.devices = append(room.devices, devID)
		switch role {
		case "switch":
			ctx.switches[tidx] = append(ctx.switches[tidx], devID)
		case "router":
			ctx.router = devID
		case "firewall-edge":
			ctx.edgeFw = devID
		}
	}
	if tidx == 1 && n > 0 {
		hv, err := g.addDevice(ctx, room, n+1, "hypervisor")
		if err != nil {
			return err
		}
		room.devices = append(room.devices, hv)
		ctx.hypervisors = append(ctx.hypervisors, hv)
		if err := g.addVMs(ctx, room, hv); err != nil {
			return err
		}
	}
	return nil
}

func (g *populator) addPanel(id, name string, meta map[string]any, x, y float64) (string, error) {
	if err := g.addObject("patch_panel", id, name, x, y, meta); err != nil {
		return "", err
	}
	g.roles[id] = "panel"
	return id, nil
}

func (g *populator) telecomMix() string {
	switch g.rnd.Intn(6) {
	case 0, 1, 2:
		return "server"
	case 3, 4:
		return "switch"
	default:
		return "firewall"
	}
}

func (g *populator) addOrdRoom(ctx *popBldg, slot int, floor string, rowGround bool) error {
	id := fmt.Sprintf("%s:r%d", ctx.bldgID, slot)
	number := fmt.Sprintf("1%02d", slot)
	if !rowGround {
		number = fmt.Sprintf("2%02d", slot)
	}
	x, y := g.roomXY(ctx, rowGround)
	if err := g.addObject("room", id, number, x, y, map[string]any{"floor_id": floor}); err != nil {
		return err
	}
	vrfs := []string{"Production", "Guest", "Voice"}
	room := &popRoom{id: id, name: number, tag: number, floor: floor, ground: rowGround, slot: slot, vrf: vrfs[slot%3]}
	ctx.rooms = append(ctx.rooms, room)
	g.roomByID[id] = room
	n := g.cfg.devMin
	if g.cfg.devMax > g.cfg.devMin {
		n += g.rnd.Intn(g.cfg.devMax - g.cfg.devMin + 1)
	}
	for d := 1; d <= n; d++ {
		devID, err := g.addDevice(ctx, room, d, g.endpointMix())
		if err != nil {
			return err
		}
		room.devices = append(room.devices, devID)
	}
	outletType := "dvo"
	outletName := fmt.Sprintf("DVO-%s-1", number)
	if slot%2 == 0 {
		outletType, outletName = "fiber", fmt.Sprintf("FIBER-%s-1", number)
	}
	outletID := id + ":o1"
	if err := g.addObject("outlet", outletID, outletName, 0, 260, map[string]any{"room_id": id, "outlet_type": outletType}); err != nil {
		return err
	}
	g.roles[outletID] = "outlet-" + outletType
	room.outlets = append(room.outlets, outletID)
	return nil
}

func (g *populator) endpointMix() string {
	switch g.rnd.Intn(12) {
	case 0, 1, 2, 3:
		return "workstation"
	case 4, 5:
		return "laptop"
	case 6, 7:
		return "pc"
	case 8:
		return "printer"
	case 9, 10:
		return "phone"
	default:
		return "ap"
	}
}

func (g *populator) rackFor(ctx *popBldg, room *popRoom) string {
	if !g.cfg.racks || !room.telecom {
		return ""
	}
	n := ctx.rackRR[room.id]
	ctx.rackRR[room.id] = n + 1
	return fmt.Sprintf("%s:k%d", room.id, n%2+1)
}

func (g *populator) addDevice(ctx *popBldg, room *popRoom, seq int, role string) (string, error) {
	id := fmt.Sprintf("%s:d%02d", room.id, seq)
	tags := map[string]string{"router": "R", "switch": "SW", "firewall": "FW", "firewall-edge": "FW", "server": "SRV", "hypervisor": "HV", "workstation": "WS", "laptop": "LT", "pc": "PC", "printer": "PRN", "phone": "IP", "ap": "AP"}
	name := fmt.Sprintf("S%02d-B%02d-%s-%s%02d", ctx.site, ctx.bldg, room.tag, tags[role], seq)
	meta := map[string]any{}
	if rack := g.rackFor(ctx, room); rack != "" {
		meta["rack_id"] = rack
	} else if room.telecom {
		meta["telecom_room_id"] = room.id
	} else {
		meta["room_id"] = room.id
	}
	meta["network"] = []any{"pop:net:prod"}
	if g.rnd.Intn(5) == 0 {
		meta["network"] = []any{"pop:net:prod", "pop:net:lab"}
	}
	switch role {
	case "router":
		t, ok := g.pick(g.routers, "router")
		g.applyType(meta, t, ok, "Router", "router")
		meta["vrfs"] = append([]string{}, populateVRFs...)
	case "switch":
		t, ok := g.pick(g.switches, "switch")
		g.applyType(meta, t, ok, "Switch", "switch")
		meta["vrfs"] = append([]string{}, populateVRFs...)
	case "firewall", "firewall-edge":
		t, ok := g.pick(g.firewalls, "firewall")
		g.applyType(meta, t, ok, "Firewall", "firewall")
	case "server":
		if t, ok := g.pick(g.servers, "server"); ok {
			meta["device_type_id"] = t.id
		}
	case "hypervisor":
		t, ok := g.pick(g.hypervisors, "hypervisor")
		g.applyType(meta, t, ok, "ESXi host", "esxi")
	default:
		if t, ok := g.pick(g.endpoints, "endpoint"); ok {
			meta["device_type_id"] = t.id
		}
	}
	x, y := float64((seq-1)%4*130), float64((seq-1)/4*130)
	if err := g.addObject("device", id, name, x, y, meta); err != nil {
		return "", err
	}
	g.roles[id] = role
	return id, nil
}

func (g *populator) addVMs(ctx *popBldg, room *popRoom, hv string) error {
	vsType, ok := g.pick(g.vswitches, "vswitch")
	if !ok {
		return nil
	}
	vsID := hv + ":vs"
	if err := g.addObject("virtual_switch", vsID, "vSwitch-1", 0, 390, map[string]any{
		"host_id": hv, "device_type_id": vsType.id, "vrfs": []string{"Production", "Management"},
	}); err != nil {
		return err
	}
	g.roles[vsID] = "vswitch"
	ctx.vswitchOf[hv] = vsID
	vmType, ok := g.pick(g.vms, "vm")
	if !ok {
		return nil
	}
	for v := 1; v <= 2; v++ {
		os := "Linux"
		if v%2 == 0 {
			os = "Windows"
		}
		vmID := fmt.Sprintf("%s:v%d", hv, v)
		if err := g.addObject("virtual_machine", vmID, fmt.Sprintf("VM-%d", v), float64(v*130), 390, map[string]any{
			"host_id": hv, "device_type_id": vmType.id, "guest_os": os,
		}); err != nil {
			return err
		}
		g.roles[vmID] = "vm"
		ctx.vmsOf[hv] = append(ctx.vmsOf[hv], vmID)
	}
	return nil
}

func popDevSeq(id string) int {
	n := 0
	if i := strings.LastIndex(id, ":d"); i >= 0 {
		fmt.Sscanf(id[i+2:], "%d", &n)
	}
	return n
}

func (g *populator) deviceIP(roomID string, seq int) string {
	var s, b int
	fmt.Sscanf(roomID, "pop:s%d:b%d", &s, &b)
	slot := 1
	if room := g.roomByID[roomID]; room != nil {
		slot = room.slot
	}
	return fmt.Sprintf("10.%d.%d.%d", s%256, ((b-1)*g.cfg.rooms+(slot-1))%256, 10+seq)
}

func (g *populator) portIP(devID string) string {
	roomID := devID
	if i := strings.LastIndex(devID, ":d"); i >= 0 {
		roomID = devID[:i]
	}
	return g.deviceIP(roomID, popDevSeq(devID))
}

func (g *populator) switchPort(sw, vrf, speed, ip string) (string, error) {
	meta := map[string]any{"speed": speed, "ip_address": ip}
	if vrf != "" {
		meta["vrfs"] = []string{vrf}
	}
	return g.port(sw, "Gi1/0/", meta)
}

func (g *populator) routerPort(router, speed, ip string, uplink bool) (string, error) {
	meta := map[string]any{"speed": speed, "ip_address": ip, "vrf_mode": "all"}
	if uplink {
		meta["interface_usage"] = "uplink"
		meta["vrf_bandwidth_bps"] = map[string]any{
			"Management": int64(100_000_000), "Production": int64(5_000_000_000),
			"Guest": int64(1_000_000_000), "Voice": int64(1_000_000_000),
		}
	}
	return g.port(router, "Gi", meta)
}

func popFiber(cable string, strands int, length float64) map[string]any {
	return map[string]any{"connection_class": "physical", "physical_type": "fiber", "medium": "fiber",
		"cable_type": cable, "connector_a": "LC", "connector_b": "LC", "strand_count": strands, "length_m": length}
}

func popCopper(cable string, length float64) map[string]any {
	return map[string]any{"connection_class": "physical", "physical_type": "copper_twisted_pair", "medium": "copper",
		"cable_type": cable, "connector_a": "RJ45", "connector_b": "RJ45", "length_m": length}
}

func (g *populator) cableBuilding(ctx *popBldg) error {
	mdf := ctx.switches[1]
	swRR := 0
	riserOK := ctx.riserGround != "" && ctx.riserL1 != ""
	nextSwitch := func() string {
		if len(mdf) == 0 {
			return ""
		}
		sw := mdf[swRR%len(mdf)]
		swRR++
		return sw
	}
	// outletPort creates a named wall-jack or fiber port on an outlet.
	outletPort := func(outlet, name, connector string) (string, error) {
		id := fmt.Sprintf("pop:p:%s:%s", strings.TrimPrefix(outlet, "pop:"), strings.ToLower(strings.ReplaceAll(name, " ", "")))
		return id, g.addInterface(id, outlet, name, map[string]any{"connector": connector})
	}
	for _, room := range ctx.rooms {
		if room.telecom {
			continue
		}
		for _, dev := range room.devices {
			ip := g.deviceIP(room.id, popDevSeq(dev))
			if g.roles[dev] == "ap" {
				sw := nextSwitch()
				if sw == "" {
					continue
				}
				ep, err := g.port(dev, "eth", map[string]any{"speed": "1G", "ip_address": ip})
				if err != nil {
					return err
				}
				sp, err := g.switchPort(sw, "Guest", "1G", g.portIP(sw))
				if err != nil {
					return err
				}
				if _, err := g.addLink(ep, sp, popCopper("Cat6A", 25)); err != nil {
					return err
				}
				continue
			}
			if ctx.panelCu == "" {
				continue
			}
			vrf := room.vrf
			switch g.roles[dev] {
			case "phone":
				vrf = "Voice"
			case "printer":
				vrf = "Production"
			}
			var swPort string
			var err error
			if sw := nextSwitch(); sw != "" {
				swPort, err = g.switchPort(sw, vrf, "1G", g.portIP(sw))
			} else if ctx.router != "" {
				swPort, err = g.routerPort(ctx.router, "1G", g.portIP(ctx.router), false)
			} else {
				continue
			}
			if err != nil {
				return err
			}
			if err := g.panelChain(ctx.panelCu, dev, "eth", "1G", ip, swPort); err != nil {
				return err
			}
		}
		for _, outlet := range room.outlets {
			if strings.HasSuffix(g.roles[outlet], "dvo") {
				if ctx.panelCu == "" {
					continue
				}
				data, err := outletPort(outlet, "Data", "RJ45")
				if err != nil {
					return err
				}
				if _, err := outletPort(outlet, "Voice", "RJ45"); err != nil {
					return err
				}
				var swPort string
				if sw := nextSwitch(); sw != "" {
					swPort, err = g.switchPort(sw, room.vrf, "1G", g.portIP(sw))
				} else if ctx.router != "" {
					swPort, err = g.routerPort(ctx.router, "1G", g.portIP(ctx.router), false)
				} else {
					continue
				}
				if err != nil {
					return err
				}
				pn, err := g.port(ctx.panelCu, "P", map[string]any{})
				if err != nil {
					return err
				}
				if _, err := g.addLink(data, pn, popCopper("Cat6A", 40)); err != nil {
					return err
				}
				pm, err := g.port(ctx.panelCu, "P", map[string]any{})
				if err != nil {
					return err
				}
				linkID, err := g.addLink(pm, swPort, popCopper("Cat6A", 3))
				if err != nil {
					return err
				}
				if err := g.addPairing(ctx.panelCu, pn, pm); err != nil {
					return err
				}
				if !room.ground && g.cfg.conduits && riserOK {
					if err := g.addCableRoute(linkID, ctx.bldgID+":cr", 0); err != nil {
						return err
					}
				}
				continue
			}
			if ctx.panelFb == "" {
				continue
			}
			fiber, err := outletPort(outlet, "Fiber 1", "LC")
			if err != nil {
				return err
			}
			if _, err := outletPort(outlet, "Fiber 2", "LC"); err != nil {
				return err
			}
			var swPort string
			if sw := nextSwitch(); sw != "" {
				swPort, err = g.switchPort(sw, "Production", "10G", g.portIP(sw))
			} else if ctx.router != "" {
				swPort, err = g.routerPort(ctx.router, "10G", g.portIP(ctx.router), false)
			} else {
				continue
			}
			if err != nil {
				return err
			}
			pn, err := g.port(ctx.panelFb, "P", map[string]any{})
			if err != nil {
				return err
			}
			if _, err := g.addLink(fiber, pn, popFiber("OS2 2-strand", 2, 40)); err != nil {
				return err
			}
			pm, err := g.port(ctx.panelFb, "P", map[string]any{})
			if err != nil {
				return err
			}
			linkID, err := g.addLink(pm, swPort, popFiber("OM4 2-strand", 2, 3))
			if err != nil {
				return err
			}
			if err := g.addPairing(ctx.panelFb, pn, pm); err != nil {
				return err
			}
			if !room.ground && g.cfg.conduits && riserOK {
				if err := g.addCableRoute(linkID, ctx.bldgID+":cr", 0); err != nil {
					return err
				}
			}
		}
	}
	for tidx := 1; tidx <= g.cfg.telecoms; tidx++ {
		room, ok := ctx.telecoms[tidx]
		if !ok {
			continue
		}
		switches := ctx.switches[tidx]
		localSwitch := ""
		if len(switches) > 0 {
			localSwitch = switches[0]
		}
		// Stack consecutive switches in the room.
		for i := 1; i < len(switches); i++ {
			a, err := g.switchPort(switches[i-1], "", "25G", g.portIP(switches[i-1]))
			if err != nil {
				return err
			}
			b, err := g.switchPort(switches[i], "", "25G", g.portIP(switches[i]))
			if err != nil {
				return err
			}
			if _, err := g.addLink(a, b, popFiber("OM4 2-strand", 2, 2)); err != nil {
				return err
			}
		}
		for _, dev := range room.devices {
			ip := g.deviceIP(room.id, popDevSeq(dev))
			switch g.roles[dev] {
			case "server":
				target, sp, err := g.accessPort(ctx, localSwitch, "Production", "10G")
				if err != nil {
					return err
				}
				if target == "" {
					continue
				}
				dp, err := g.port(dev, "eth", map[string]any{"speed": "10G", "ip_address": ip})
				if err != nil {
					return err
				}
				if _, err := g.addLink(dp, sp, popFiber("OM4 2-strand", 2, 5)); err != nil {
					return err
				}
			case "hypervisor":
				if err := g.cableHypervisor(ctx, room, dev, localSwitch); err != nil {
					return err
				}
			case "firewall":
				if localSwitch == "" {
					continue
				}
				lan, err := g.port(dev, "eth", map[string]any{"speed": "10G", "ip_address": ip})
				if err != nil {
					return err
				}
				sp, err := g.switchPort(localSwitch, "Production", "10G", g.portIP(localSwitch))
				if err != nil {
					return err
				}
				if _, err := g.addLink(lan, sp, popCopper("Cat6A", 3)); err != nil {
					return err
				}
				if _, err := g.port(dev, "eth", map[string]any{"speed": "10G", "ip_address": ip}); err != nil {
					return err
				}
			}
		}
		// Uplink every switch to the building router (the site core in building 1).
		parent := ctx.router
		for _, sw := range switches {
			if parent == "" {
				break
			}
			a, err := g.switchPort(sw, "", "10G", g.portIP(sw))
			if err != nil {
				return err
			}
			b, err := g.routerPort(parent, "10G", g.portIP(parent), false)
			if err != nil {
				return err
			}
			if _, err := g.addLink(a, b, popFiber("OM4 12-strand", 12, 60)); err != nil {
				return err
			}
		}
	}
	// Building router uplinks to the site core over the ring duct.
	if ctx.bldg > 1 && ctx.router != "" && ctx.core != "" {
		up, err := g.routerPort(ctx.router, "10G", g.portIP(ctx.router), true)
		if err != nil {
			return err
		}
		cp, err := g.routerPort(ctx.core, "10G", g.portIP(ctx.core), false)
		if err != nil {
			return err
		}
		linkID, err := g.addLink(up, cp, popFiber("OS2 24-strand", 24, 300))
		if err != nil {
			return err
		}
		if g.cfg.conduits {
			seq := 0
			for gap := ctx.bldg - 1; gap >= 1; gap-- {
				for _, conduit := range g.ringConduits(ctx.siteID, gap) {
					if err := g.addCableRoute(linkID, conduit, seq); err != nil {
						return err
					}
					seq++
				}
			}
		}
	}
	// Edge firewall between the site core and the WAN transports.
	if ctx.edgeFw != "" && ctx.router != "" {
		ip := g.portIP(ctx.edgeFw)
		lan, err := g.port(ctx.edgeFw, "eth", map[string]any{"speed": "10G", "ip_address": ip})
		if err != nil {
			return err
		}
		cp, err := g.routerPort(ctx.router, "10G", g.portIP(ctx.router), false)
		if err != nil {
			return err
		}
		if _, err := g.addLink(lan, cp, popCopper("Cat6A", 2)); err != nil {
			return err
		}
		if g.cfg.transports {
			for _, service := range []string{"pop:svc:internet", "pop:svc:mpls", "pop:svc:pls"} {
				wp, err := g.port(ctx.edgeFw, "eth", map[string]any{"speed": "10G", "ip_address": ip})
				if err != nil {
					return err
				}
				g.circuitSeq++
				if err := g.attachService(wp, service, fmt.Sprintf("CKT-%05d", g.circuitSeq), "10Gbps", "Site uplink"); err != nil {
					return err
				}
			}
		}
	}
	// Riser fiber between the ground and upper telecom rooms.
	if riserOK {
		gRoom, lRoom := g.roomByID[ctx.riserGround], g.roomByID[ctx.riserL1]
		if gRoom != nil && lRoom != nil && len(ctx.switches[gRoom.tidx]) > 0 && len(ctx.switches[lRoom.tidx]) > 0 {
			a, err := g.switchPort(ctx.switches[gRoom.tidx][0], "", "10G", g.portIP(ctx.switches[gRoom.tidx][0]))
			if err != nil {
				return err
			}
			b, err := g.switchPort(ctx.switches[lRoom.tidx][0], "", "10G", g.portIP(ctx.switches[lRoom.tidx][0]))
			if err != nil {
				return err
			}
			linkID, err := g.addLink(a, b, popFiber("OS2 12-strand", 12, 45))
			if err != nil {
				return err
			}
			if g.cfg.conduits {
				if err := g.addCableRoute(linkID, ctx.bldgID+":cr", 0); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// panelChain wires device -> panel front -> panel rear -> switch port, pairing
// the two panel ports like a horizontal cross-connect.
func (g *populator) panelChain(panel, dev, prefix, speed, ip, swPort string) error {
	ep, err := g.port(dev, prefix, map[string]any{"speed": speed, "ip_address": ip})
	if err != nil {
		return err
	}
	pn, err := g.port(panel, "P", map[string]any{})
	if err != nil {
		return err
	}
	if _, err := g.addLink(ep, pn, popCopper("Cat6A", 40)); err != nil {
		return err
	}
	pm, err := g.port(panel, "P", map[string]any{})
	if err != nil {
		return err
	}
	if _, err := g.addLink(pm, swPort, popCopper("Cat6A", 3)); err != nil {
		return err
	}
	return g.addPairing(panel, pn, pm)
}

// accessPort returns a switch access port, falling back to a router port when
// the room has no switch.
func (g *populator) accessPort(ctx *popBldg, localSwitch, vrf, speed string) (string, string, error) {
	if localSwitch != "" {
		sp, err := g.switchPort(localSwitch, vrf, speed, g.portIP(localSwitch))
		return localSwitch, sp, err
	}
	if ctx.router != "" {
		rp, err := g.routerPort(ctx.router, speed, g.portIP(ctx.router), false)
		return ctx.router, rp, err
	}
	return "", "", nil
}

func (g *populator) cableHypervisor(ctx *popBldg, room *popRoom, hv, localSwitch string) error {
	ip := g.deviceIP(room.id, popDevSeq(hv))
	switches := ctx.switches[room.tidx]
	mgmtTarget := localSwitch
	dataTarget := localSwitch
	if len(switches) > 1 {
		dataTarget = switches[1]
	}
	if mgmtTarget == "" {
		mgmtTarget, dataTarget = ctx.router, ctx.router
	}
	if mgmtTarget == "" {
		return nil
	}
	mgmt, err := g.port(hv, "vmnic", map[string]any{"speed": "1G", "ip_address": ip})
	if err != nil {
		return err
	}
	var msp string
	if mgmtTarget == ctx.router && mgmtTarget != localSwitch {
		msp, err = g.routerPort(mgmtTarget, "1G", g.portIP(mgmtTarget), false)
	} else {
		msp, err = g.switchPort(mgmtTarget, "Management", "1G", g.portIP(mgmtTarget))
	}
	if err != nil {
		return err
	}
	if _, err := g.addLink(mgmt, msp, popCopper("Cat6A", 3)); err != nil {
		return err
	}
	data, err := g.port(hv, "vmnic", map[string]any{"speed": "25G", "ip_address": ip})
	if err != nil {
		return err
	}
	var dsp string
	if dataTarget == ctx.router && dataTarget != localSwitch {
		dsp, err = g.routerPort(dataTarget, "25G", g.portIP(dataTarget), false)
	} else {
		dsp, err = g.switchPort(dataTarget, "Production", "10G", g.portIP(dataTarget))
	}
	if err != nil {
		return err
	}
	if _, err := g.addLink(data, dsp, popFiber("OM4 2-strand", 2, 5)); err != nil {
		return err
	}
	vs := ctx.vswitchOf[hv]
	if vs == "" {
		return nil
	}
	up, err := g.port(hv, "vmnic", map[string]any{"speed": "10G", "ip_address": ip})
	if err != nil {
		return err
	}
	vsu, err := g.port(vs, "Uplink", map[string]any{})
	if err != nil {
		return err
	}
	if _, err := g.addLink(up, vsu, map[string]any{"connection_class": "virtual", "virtual_type": "ethernet"}); err != nil {
		return err
	}
	for i, vm := range ctx.vmsOf[hv] {
		vp, err := g.port(vm, "eth", map[string]any{})
		if err != nil {
			return err
		}
		vsp, err := g.port(vs, "Port", map[string]any{})
		if err != nil {
			return err
		}
		spec := map[string]any{"connection_class": "virtual", "virtual_type": "ethernet", "network_name": "VM Network"}
		if i%2 == 0 {
			spec = map[string]any{"connection_class": "virtual", "virtual_type": "vlan", "vlan_id": 10 + i*10, "network_name": "VLAN trunk"}
		}
		if _, err := g.addLink(vp, vsp, spec); err != nil {
			return err
		}
	}
	return nil
}

// ringConduits returns the duct ids joining building gap to gap+1, routing the
// first gap through the site manhole.
func (g *populator) ringConduits(siteID string, gap int) []string {
	if gap == 1 {
		return []string{siteID + ":cm1a", siteID + ":cm1b"}
	}
	return []string{fmt.Sprintf("%s:cc%d", siteID, gap)}
}

func (g *populator) bldgID(siteID string, b int) string {
	return fmt.Sprintf("%s:b%02d", siteID, b)
}

func (g *populator) midpoint(a, b string) (float64, float64) {
	pa, pb := g.coords[a], g.coords[b]
	return (pa[0] + pb[0]) / 2, (pa[1] + pb[1]) / 2
}

func (g *populator) addSiteConduits(s int, siteID string) error {
	bldgs := g.cfg.buildings
	if bldgs > 1 {
		mh := siteID + ":mh1"
		x, y := g.midpoint(g.bldgID(siteID, 1), g.bldgID(siteID, 2))
		if err := g.addObject("manhole", mh, "MH-1", x, y-60, map[string]any{"site_id": siteID, "building_id": g.bldgID(siteID, 1)}); err != nil {
			return err
		}
		for gap := 1; gap <= bldgs; gap++ {
			a, b := gap, gap%bldgs+1
			aID, bID := g.bldgID(siteID, a), g.bldgID(siteID, b)
			name := fmt.Sprintf("B%02d-B%02d duct", a, b)
			if gap == 1 {
				x, y := g.midpoint(aID, mh)
				if err := g.addObject("conduit", siteID+":cm1a", "B01-MH1 duct", x, y, map[string]any{"endpoint_a_id": aID, "endpoint_b_id": mh}); err != nil {
					return err
				}
				x, y = g.midpoint(mh, bID)
				if err := g.addObject("conduit", siteID+":cm1b", "MH1-B02 duct", x, y, map[string]any{"endpoint_a_id": mh, "endpoint_b_id": bID}); err != nil {
					return err
				}
				continue
			}
			x, y := g.midpoint(aID, bID)
			if err := g.addObject("conduit", fmt.Sprintf("%s:cc%d", siteID, gap), name, x, y, map[string]any{"endpoint_a_id": aID, "endpoint_b_id": bID}); err != nil {
				return err
			}
		}
	}
	for b := 1; b <= bldgs; b++ {
		bldgID := g.bldgID(siteID, b)
		var ground, upper string
		for tidx := 1; tidx <= g.cfg.telecoms; tidx++ {
			room := g.roomByID[fmt.Sprintf("%s:t%d", bldgID, tidx)]
			if room == nil {
				continue
			}
			if room.ground && ground == "" {
				ground = room.id
			}
			if !room.ground && upper == "" {
				upper = room.id
			}
		}
		if ground == "" || upper == "" {
			continue
		}
		x, y := g.midpoint(ground, upper)
		letter := string(rune('A' + b - 1))
		if err := g.addObject("conduit", bldgID+":cr", letter+" riser", x, y, map[string]any{"endpoint_a_id": ground, "endpoint_b_id": upper}); err != nil {
			return err
		}
	}
	return nil
}

func (g *populator) addTunnel(s int, hq, site string) error {
	proto := "gre"
	if s > 1 && populateSiteRoles[(s-2)%len(populateSiteRoles)] == "cloud_colo" {
		proto = "wireguard"
	}
	local := g.portIP(site)
	remote := g.portIP(hq)
	hp, err := g.port(hq, "Tun", map[string]any{"interface_type": "logical", "vrf_mode": "all", "ip_address": remote, "description": fmt.Sprintf("Tunnel to site %d", s)})
	if err != nil {
		return err
	}
	sp, err := g.port(site, "Tun", map[string]any{"interface_type": "logical", "vrf_mode": "all", "ip_address": local, "description": "Tunnel to HQ"})
	if err != nil {
		return err
	}
	_, err = g.addLink(hp, sp, map[string]any{"connection_class": "logical_tunnel", "tunnel_protocol": proto,
		"tunnel_mode": "tunnel", "local_endpoint": remote, "remote_endpoint": local, "label": fmt.Sprintf("Site %d WAN", s)})
	return err
}
