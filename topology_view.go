package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type topologyItem struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Data    map[string]any `json:"data"`
	Version int            `json:"version"`
	X       float64        `json:"x"`
	Y       float64        `json:"y"`
}
type topologyNode struct {
	topologyItem
	Boundary bool   `json:"boundary"`
	Parent   string `json:"parent,omitempty"`
	Count    int    `json:"count"`
	Warning  string `json:"warning,omitempty"`
}
type topologyEdge struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Target   string   `json:"target"`
	Category string   `json:"category"`
	Label    string   `json:"label"`
	Count    int      `json:"count"`
	ObjectID string   `json:"object_id,omitempty"`
	Refs     []string `json:"-"`
}
type topologyConnection struct {
	ID, Source, Target, SourcePort, TargetPort, Category, Label string
	Data                                                        map[string]any
}
type topologyScene struct {
	Nodes   []topologyNode
	Edges   []topologyEdge
	ByID    map[string]topologyNode
	ByEdge  map[string]topologyEdge
	Scope   *topologyItem
	Actions []string
	Path    []topologyItem
}

// The server shares one immutable projection snapshot per durable event sequence.
// Browser responses stay bounded regardless of inventory size. Scene caches are
// bounded and replaced after commits; neither camera nor user drafts are shared.
type topologySnapshot struct {
	InterfaceWarnings map[string]int
	DB                *sql.DB
	Seq               int64
	Objects           map[string]topologyItem
	Order             []string
	Names             map[string]string
	Parents           map[string]string
	Ancestors         map[string][]string
	Children          map[string][]string
	Counts            map[string]int
	Connections       []topologyConnection
	Relationships     []topologyRelationship
	CableRoutes       map[string][]string
	ConduitCables     map[string][]string
	Scenes            map[string]*topologyScene
}

func (s *topologySnapshot) ref(kind, ref string) string {
	if o, ok := s.Objects[ref]; ok && o.Kind == kind {
		return ref
	}
	return s.Names[kind+"|"+ref]
}
func (s *topologySnapshot) ancestor(id, kind string) string {
	for _, x := range s.Ancestors[id] {
		if s.Objects[x].Kind == kind {
			return x
		}
	}
	return ""
}
func (s *topologySnapshot) within(id, scope string) bool {
	if id == scope {
		return true
	}
	for _, x := range s.Ancestors[id] {
		if x == scope {
			return true
		}
	}
	return false
}
func loadTopology(tx *sql.Tx, db *sql.DB, seq int64) (*topologySnapshot, error) {
	s := &topologySnapshot{DB: db, Seq: seq, Objects: map[string]topologyItem{}, Names: map[string]string{}, Parents: map[string]string{}, Ancestors: map[string][]string{}, Children: map[string][]string{}, Counts: map[string]int{}, CableRoutes: map[string][]string{}, ConduitCables: map[string][]string{}, Scenes: map[string]*topologyScene{}}
	rows, e := tx.Query(`SELECT id,kind,name,data,version,x,y FROM objects ORDER BY id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var o topologyItem
		var raw string
		if e = rows.Scan(&o.ID, &o.Kind, &o.Name, &raw, &o.Version, &o.X, &o.Y); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &o.Data); e != nil {
			rows.Close()
			return nil, e
		}
		if o.Data == nil {
			o.Data = map[string]any{}
		}
		s.Objects[o.ID] = o
		s.Order = append(s.Order, o.ID)
		name := o.Kind + "|" + o.Name
		if _, ok := s.Names[name]; ok {
			s.Names[name] = ""
		} else {
			s.Names[name] = o.ID
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, id := range s.Order {
		o := s.Objects[id]
		if o.Kind == "service" {
			continue
		}
		keys := []struct{ key, kind string }{{"host_id", "device"}, {"rack_id", "rack"}, {"telecom_room_id", "telecom_room"}, {"room_id", "room"}, {"floor_id", "floor"}, {"building_id", "building"}, {"site_id", "site"}}
		for _, p := range keys {
			if o.Kind == p.kind {
				continue
			}
			if parent := s.ref(p.kind, topologyText(o.Data, p.key)); parent != "" && parent != id {
				s.Parents[id] = parent
				s.Children[parent] = append(s.Children[parent], id)
				break
			}
		}
	}
	for _, id := range s.Order {
		seen := map[string]bool{}
		for p := id; p != "" && !seen[p]; p = s.Parents[p] {
			seen[p] = true
			s.Ancestors[id] = append(s.Ancestors[id], p)
			if p != id {
				s.Counts[p]++
			}
		}
	}
	s.InterfaceWarnings = map[string]int{}
	rows, e = tx.Query(`SELECT device_id,data FROM interfaces ORDER BY device_id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var device, raw string
		if e = rows.Scan(&device, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		owner, found := s.Objects[device]
		if !found {
			continue
		}
		port, decodeErr := decodeInterfaceMetadata(raw)
		if decodeErr != nil {
			s.InterfaceWarnings[device]++
			continue
		}
		if err := normalizeInterfaceVRFs(owner.Data, port); err != nil {
			s.InterfaceWarnings[device]++
			continue
		}
		issues, err := normalizeInterfaceCapacity(owner.Kind, owner.Data, port, false)
		if err != nil || len(issues) > 0 {
			s.InterfaceWarnings[device]++
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(`SELECT l.id,si.device_id,ti.device_id,l.source_interface,l.target_interface,l.data FROM links l JOIN interfaces si ON si.id=l.source_interface JOIN interfaces ti ON ti.id=l.target_interface ORDER BY l.id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var c topologyConnection
		var raw string
		if e = rows.Scan(&c.ID, &c.Source, &c.Target, &c.SourcePort, &c.TargetPort, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &c.Data); e != nil {
			rows.Close()
			return nil, e
		}
		class := topologyText(c.Data, "connection_class")
		if class == "" {
			class = "physical"
		}
		c.Category = class + ":" + topologyText(c.Data, "medium") + ":" + topologyText(c.Data, "service_type")
		c.Label = topologyText(c.Data, "label")
		s.Connections = append(s.Connections, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(`SELECT link_id,infrastructure_id FROM cable_routes ORDER BY link_id,sequence`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var link, infra string
		if e = rows.Scan(&link, &infra); e != nil {
			rows.Close()
			return nil, e
		}
		s.CableRoutes[link] = append(s.CableRoutes[link], infra)
		s.ConduitCables[infra] = append(s.ConduitCables[infra], link)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = tx.Query(`SELECT id,source_id,target_id,relationship_type,version,label FROM relationships ORDER BY id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var x topologyRelationship
		if e = rows.Scan(&x.ID, &x.Source, &x.Target, &x.Type, &x.Version, &x.Label); e != nil {
			rows.Close()
			return nil, e
		}
		s.Relationships = append(s.Relationships, x)
	}
	e = rows.Err()
	rows.Close()
	hostedPairs := map[string]bool{}
	for _, r := range s.Relationships {
		pair := r.Source + "|" + r.Target
		if r.Type == "hosts" && !hostedPairs[pair] {
			hostedPairs[pair] = true
			s.Counts[r.Source]++
		}
	}
	return s, e
}
func topologyActions(kind string) []string {
	switch kind {
	case "global":
		return []string{"site", "service", "relationship"}
	case "service":
		return []string{"service", "relationship", "attachment"}
	case "site":
		return []string{"building", "generator", "ups", "conduit", "relationship", "attachment"}
	case "building":
		return []string{"floor", "room", "telecom_room", "generator", "ups", "conduit"}
	case "floor":
		return []string{"room", "telecom_room", "rack", "device", "patch_panel", "dvo", "fiber", "cable", "conduit"}
	case "room":
		return []string{"dvo", "fiber", "device", "rack", "ups", "cable", "conduit"}
	case "telecom_room":
		return []string{"rack", "device", "patch_panel", "ups", "dvo", "fiber", "cable", "conduit"}
	case "rack":
		return []string{"device", "patch_panel", "interface", "cable"}
	case "conduit":
		return []string{"assign_cable", "cable"}
	}
	return []string{}
}
func (s *topologySnapshot) scene(scope, kind, network, category, roomType string) (*topologyScene, error) {
	cacheKey := kind + "|" + scope + "|" + network + "|" + category + "|" + roomType
	if cached := s.Scenes[cacheKey]; cached != nil {
		return cached, nil
	}
	scene := &topologyScene{Nodes: []topologyNode{}, Edges: []topologyEdge{}, ByID: map[string]topologyNode{}, ByEdge: map[string]topologyEdge{}, Actions: topologyActions(kind), Path: []topologyItem{}}
	if kind != "global" {
		o, ok := s.Objects[scope]
		if !ok || o.Kind != kind {
			return nil, fmt.Errorf("scope is missing or its type changed")
		}
		scene.Scope = &o
		for i := len(s.Ancestors[scope]) - 1; i >= 0; i-- {
			scene.Path = append(scene.Path, s.Objects[s.Ancestors[scope][i]])
		}
	}
	if len(scene.Actions) == 0 {
		return nil, fmt.Errorf("unsupported scope kind")
	}
	base := map[string]bool{}
	relevant := map[string]bool{}
	if kind == "service" {
		relevant[scope] = true
		for _, r := range s.Relationships {
			if r.Source == scope && r.Type == "hosts" {
				base[r.Target] = true
				relevant[r.Target] = true
			}
		}
	}
	for _, id := range s.Order {
		o := s.Objects[id]
		switch kind {
		case "global":
			if o.Kind == "site" || (o.Kind == "service" && topologyText(o.Data, "service_category") != "cloud_resource") {
				base[id] = true
			}
		case "site":
			if (o.Kind == "building" || o.Kind == "generator" || o.Kind == "ups" && s.Parents[id] == s.ancestor(id, "building")) && s.within(id, scope) {
				base[id] = true
			}
		case "building":
			if (o.Kind == "floor" || o.Kind == "room" || o.Kind == "telecom_room" || o.Kind == "generator" || o.Kind == "ups" && s.Parents[id] == scope) && s.within(id, scope) {
				base[id] = true
			}
		case "floor":
			if s.within(id, scope) && id != scope {
				if o.Kind == "room" || o.Kind == "telecom_room" || s.Parents[id] == scope && (o.Kind == "rack" || o.Kind == "device" || o.Kind == "patch_panel" || o.Kind == "outlet") {
					base[id] = true
				}
			}
		case "room", "telecom_room", "rack":
			if s.Parents[id] == scope && o.Kind != "conduit" {
				base[id] = true
			}
		}
	}
	if kind == "conduit" {
		o := s.Objects[scope]
		for _, key := range []string{"endpoint_a_id", "endpoint_b_id"} {
			if id := topologyText(o.Data, key); s.Objects[id].ID != "" {
				base[id] = true
				relevant[id] = true
			}
		}
		for _, c := range s.Connections {
			for _, route := range s.CableRoutes[c.ID] {
				if route == scope {
					base[c.Source] = true
					base[c.Target] = true
					relevant[c.Source] = true
					relevant[c.Target] = true
					break
				}
			}
		}
	}
	for id := range base {
		if roomType != "" && (s.Objects[id].Kind == "room" || s.Objects[id].Kind == "telecom_room") && s.Objects[id].Kind != roomType {
			delete(base, id)
		}
	}
	matchesNetwork := func(id string) bool {
		if network == "" {
			return true
		}
		for _, ancestor := range s.Ancestors[id] {
			switch values := s.Objects[ancestor].Data["network"].(type) {
			case string:
				for _, v := range strings.Split(values, ",") {
					if strings.TrimSpace(v) == network {
						return true
					}
				}
			case []any:
				for _, v := range values {
					if v == network {
						return true
					}
				}
			}
		}
		return false
	}
	if network != "" {
		keep := map[string]bool{}
		for _, id := range s.Order {
			if matchesNetwork(id) {
				for _, p := range s.Ancestors[id] {
					keep[p] = true
				}
			}
		}
		for id := range base {
			if !keep[id] && s.Objects[id].Kind != "service" && !((s.Objects[id].Kind == "generator" || s.Objects[id].Kind == "ups") && keep[s.Parents[id]]) {
				delete(base, id)
			}
		}
	}
	if category != "" {
		for id := range base {
			if s.Objects[id].Kind == "service" && topologyText(s.Objects[id].Data, "service_category") != category {
				delete(base, id)
			}
		}
	}
	addNode := func(id string, boundary bool) string {
		if id == "" {
			return ""
		}
		o, ok := s.Objects[id]
		if !ok {
			return ""
		}
		if old, ok := scene.ByID[id]; ok {
			if !boundary && old.Boundary {
				old.Boundary = false
				scene.ByID[id] = old
			}
			return id
		}
		n := topologyNode{topologyItem: o, Boundary: boundary, Count: s.Counts[id]}
		if count := s.InterfaceWarnings[id]; count > 0 {
			n.Warning = fmt.Sprintf("%d interface(s) need speed or bandwidth details", count)
		}
		if kind == "building" && (o.Kind == "room" || o.Kind == "telecom_room") {
			floor := s.ancestor(id, "floor")
			if base[floor] {
				n.Parent = floor
			}
		}
		scene.ByID[id] = n
		return id
	}
	for id := range base {
		addNode(id, false)
	}
	// A source in the scope collapses to its closest visible ancestor; an outside
	// endpoint collapses to the closest meaningful boundary for the current level.
	project := func(id string) string {
		if _, ok := s.Objects[id]; !ok {
			return ""
		}
		if base[id] {
			return addNode(id, false)
		}
		for _, p := range s.Ancestors[id] {
			if base[p] {
				return addNode(p, false)
			}
		}
		if s.Objects[id].Kind == "service" {
			if category != "" && topologyText(s.Objects[id].Data, "service_category") != category {
				return ""
			}
			return addNode(id, true)
		}
		if kind == "global" {
			return addNode(s.ancestor(id, "site"), true)
		}
		if kind == "service" || kind == "conduit" {
			if site := s.ancestor(id, "site"); site != "" {
				return addNode(site, true)
			}
			return addNode(id, true)
		}
		if s.within(id, scope) {
			return addNode(scope, false)
		}
		localSite := s.ancestor(scope, "site")
		localBuilding := s.ancestor(scope, "building")
		localFloor := s.ancestor(scope, "floor")
		if localSite != "" && s.ancestor(id, "site") != localSite {
			return addNode(s.ancestor(id, "site"), true)
		}
		if localBuilding != "" && s.ancestor(id, "building") != localBuilding {
			return addNode(s.ancestor(id, "building"), true)
		}
		if localFloor != "" && s.ancestor(id, "floor") != localFloor {
			return addNode(s.ancestor(id, "floor"), true)
		}
		for _, k := range []string{"telecom_room", "room", "floor", "building", "site"} {
			if p := s.ancestor(id, k); p != "" && p != scope {
				return addNode(p, true)
			}
		}
		return addNode(id, true)
	}
	inScope := func(id string) bool {
		if kind == "global" {
			return true
		}
		if kind == "service" || kind == "conduit" {
			return relevant[id]
		}
		return s.within(id, scope)
	}
	appendEdge := func(source, target, category, label, ref, objectID string) {
		if source == "" || target == "" || source == target {
			return
		}
		if !strings.HasPrefix(category, "relationship:") && category != "power" && source > target {
			source, target = target, source
		}
		sum := sha256.Sum256([]byte(cacheKey + "|" + source + "|" + target + "|" + category + "|" + objectID))
		id := "summary:" + hex.EncodeToString(sum[:12])
		edge := scene.ByEdge[id]
		if edge.ID == "" {
			edge = topologyEdge{ID: id, Source: source, Target: target, Category: category, Label: label, ObjectID: objectID, Refs: []string{}}
		}
		edge.Count++
		edge.Refs = append(edge.Refs, ref)
		scene.ByEdge[id] = edge
	}
	for _, c := range s.Connections {
		if kind == "conduit" {
			found := false
			for _, route := range s.CableRoutes[c.ID] {
				if route == scope {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if !inScope(c.Source) && !inScope(c.Target) {
			continue
		}
		if !matchesNetwork(c.Source) && !matchesNetwork(c.Target) {
			continue
		}
		a, b := project(c.Source), project(c.Target)
		label := c.Label
		if label == "" {
			label = strings.ReplaceAll(c.Category, ":", " · ")
		}
		appendEdge(a, b, c.Category, label, "link:"+c.ID, "")
	}
	if kind != "conduit" {
		for _, r := range s.Relationships {
			if !inScope(r.Source) && !inScope(r.Target) {
				continue
			}
			if !matchesNetwork(r.Source) && !matchesNetwork(r.Target) {
				continue
			}
			a, b := project(r.Source), project(r.Target)
			label := r.Label
			if label == "" {
				label = strings.ReplaceAll(r.Type, "_", " ")
			}
			appendEdge(a, b, "relationship:"+r.Type, label, "relationship:"+r.ID, "")
		}
	}
	if kind != "service" && kind != "rack" {
		for _, id := range s.Order {
			o := s.Objects[id]
			if o.Kind != "conduit" {
				continue
			}
			if kind == "conduit" && id != scope {
				continue
			}
			a, b := topologyText(o.Data, "endpoint_a_id"), topologyText(o.Data, "endpoint_b_id")
			if !inScope(a) && !inScope(b) {
				continue
			}
			if a == "" || b == "" {
				if (kind != "global" && s.within(id, scope)) || id == scope {
					addNode(id, false)
					n := scene.ByID[id]
					n.Warning = "Conduit endpoints are incomplete"
					scene.ByID[id] = n
				}
				continue
			}
			pa, pb := project(a), project(b)
			appendEdge(pa, pb, "conduit", o.Name, "conduit:"+id, id)
		}
	}

	if kind != "global" && kind != "service" && kind != "conduit" {
		for id := range base {
			o := s.Objects[id]
			if o.Kind == "generator" || o.Kind == "ups" {
				parent := s.Parents[id]
				if parent != "" {
					p := addNode(parent, !base[parent])
					appendEdge(id, p, "power", "Supplies power", o.Kind+":"+id, id)
				}
			}
		}
	}
	// Do not retain boundary nodes introduced only by a hidden internal edge.
	used := map[string]bool{}
	for _, e := range scene.ByEdge {
		used[e.Source] = true
		used[e.Target] = true
	}
	for id, n := range scene.ByID {
		if !n.Boundary || used[id] {
			scene.Nodes = append(scene.Nodes, n)
		} else {
			delete(scene.ByID, id)
		}
	}
	for _, e := range scene.ByEdge {
		scene.Edges = append(scene.Edges, e)
	}
	sort.Slice(scene.Nodes, func(i, j int) bool { return scene.Nodes[i].ID < scene.Nodes[j].ID })
	sort.Slice(scene.Edges, func(i, j int) bool { return scene.Edges[i].ID < scene.Edges[j].ID })
	if len(s.Scenes) >= 32 {
		s.Scenes = map[string]*topologyScene{}
	}
	s.Scenes[cacheKey] = scene
	return scene, nil
}
func (a *App) topologyView(w http.ResponseWriter, r *http.Request, db *sql.DB, p principal) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kind := r.URL.Query().Get("scope_kind")
	if kind == "" {
		kind = "global"
	}
	id := r.URL.Query().Get("scope_id")
	a.topologyMu.Lock()
	locked := true
	release := func() {
		if locked {
			locked = false
			a.topologyMu.Unlock()
		}
	}
	defer release()
	tx, e := db.Begin()
	if e != nil {
		http.Error(w, "database busy", 503)
		return
	}
	defer tx.Rollback()
	respond := func(value any) { _ = tx.Rollback(); release(); json.NewEncoder(w).Encode(value) }
	var seq int64
	if e = tx.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq); e != nil {
		http.Error(w, "event cursor unavailable", 500)
		return
	}
	s := a.topology
	if s != nil && s.DB == db && s.Seq != seq {
		rows, err := tx.Query(`SELECT payload FROM events WHERE seq>? ORDER BY seq LIMIT 1001`, s.Seq)
		if err != nil {
			http.Error(w, "projection event lookup failed", 500)
			return
		}
		onlyLayouts := true
		count := 0
		for rows.Next() {
			var raw string
			if rows.Scan(&raw) != nil {
				onlyLayouts = false
				break
			}
			var event map[string]any
			_ = json.Unmarshal([]byte(raw), &event)
			if event["type"] != "layout" {
				onlyLayouts = false
			}
			count++
		}
		if rows.Err() != nil {
			onlyLayouts = false
		}
		rows.Close()
		if onlyLayouts && count <= 1000 {
			s.Seq = seq
		}
	}
	if s == nil || s.DB != db || s.Seq != seq {
		s, e = loadTopology(tx, db, seq)
		if e != nil {
			http.Error(w, "topology projection failed: "+e.Error(), 500)
			return
		}
		a.topology = s
	}

	if r.URL.Path == "/api/topology/cables" {
		items := []map[string]string{}
		q := strings.ToLower(r.URL.Query().Get("q"))
		for _, c := range s.Connections {
			class := topologyText(c.Data, "connection_class")
			if class != "" && class != "physical" {
				continue
			}
			label := s.Objects[c.Source].Name + " → " + s.Objects[c.Target].Name
			if c.Label != "" {
				label = c.Label + " · " + label
			}
			if q != "" && !strings.Contains(strings.ToLower(label), q) {
				continue
			}
			items = append(items, map[string]string{"id": c.ID, "label": label})
			if len(items) == 100 {
				break
			}
		}
		respond(map[string]any{"items": items, "sequence": seq})
		return
	}
	if r.URL.Path == "/api/topology/choices" {
		kinds := map[string]bool{}
		for _, k := range strings.Split(r.URL.Query().Get("kinds"), ",") {
			kinds[k] = true
		}
		parent := r.URL.Query().Get("parent_id")
		q := strings.ToLower(r.URL.Query().Get("q"))
		after := r.URL.Query().Get("after")
		items := []map[string]any{}
		next := ""
		for _, objectID := range s.Order {
			if objectID <= after {
				continue
			}
			o := s.Objects[objectID]
			if r.URL.Query().Get("hypervisor") == "1" && (o.Kind != "device" || !isHypervisorMeta(o.Data)) {
				continue
			}
			if len(kinds) > 0 && !kinds[""] && !kinds[o.Kind] {
				continue
			}
			if parent != "" && !s.within(o.ID, parent) {
				continue
			}
			if category := r.URL.Query().Get("service_category"); category != "" && !containsTopologyString(strings.Split(category, ","), topologyText(o.Data, "service_category")) {
				continue
			}
			names := []string{}
			for i := len(s.Ancestors[o.ID]) - 1; i >= 0; i-- {
				names = append(names, s.Objects[s.Ancestors[o.ID][i]].Name)
			}
			path := strings.Join(names, " / ")
			if o.Kind == "service" {
				for _, key := range []string{"provider", "account", "region"} {
					if value := topologyText(o.Data, key); value != "" {
						path += " · " + value
					}
				}
			}
			if q != "" && !strings.Contains(strings.ToLower(path), q) && !strings.Contains(strings.ToLower(o.Name), q) {
				continue
			}
			if len(items) == 100 {
				next = items[99]["id"].(string)
				break
			}
			items = append(items, map[string]any{"id": o.ID, "kind": o.Kind, "name": o.Name, "path": path, "data": o.Data, "version": o.Version})
		}
		respond(map[string]any{"items": items, "next_cursor": next, "sequence": seq})
		return
	}
	if expected := r.URL.Query().Get("sequence"); expected != "" && expected != strconv.FormatInt(seq, 10) {
		http.Error(w, "topology changed; restart pagination", 409)
		return
	}
	scene, e := s.scene(id, kind, r.URL.Query().Get("network"), r.URL.Query().Get("service_category"), r.URL.Query().Get("room_type"))
	if e != nil {
		http.Error(w, e.Error(), 404)
		return
	}
	limit := 250
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > 500 {
		limit = 500
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("after"))
	if offset < 0 {
		offset = 0
	}
	if r.URL.Path == "/api/topology/details" {
		edge, ok := scene.ByEdge[r.URL.Query().Get("edge_id")]
		if !ok {
			http.Error(w, "summary no longer exists", 404)
			return
		}
		items := []any{}
		last := offset + limit
		if last > len(edge.Refs) {
			last = len(edge.Refs)
		}
		refs := map[string]bool{}
		for i := offset; i < last; i++ {
			refs[edge.Refs[i]] = true
		}
		for _, c := range s.Connections {
			if refs["link:"+c.ID] {
				items = append(items, map[string]any{"type": "link", "id": c.ID, "source": s.Objects[c.Source], "target": s.Objects[c.Target], "source_interface": c.SourcePort, "target_interface": c.TargetPort, "data": c.Data, "route": s.CableRoutes[c.ID]})
			}
		}
		for _, relation := range s.Relationships {
			if refs["relationship:"+relation.ID] {
				items = append(items, map[string]any{"type": "relationship", "relationship": relation, "source": s.Objects[relation.Source], "target": s.Objects[relation.Target]})
			}
		}
		for ref := range refs {
			if strings.HasPrefix(ref, "conduit:") {
				conduit := strings.TrimPrefix(ref, "conduit:")
				items = append(items, map[string]any{"type": "conduit", "object": s.Objects[conduit], "cable_count": len(s.ConduitCables[conduit])})
			}
		}
		next := ""
		if last < len(edge.Refs) {
			next = strconv.Itoa(last)
		}
		respond(map[string]any{"items": items, "next_cursor": next, "sequence": seq, "count": len(edge.Refs)})
		return
	}
	group := "scope:" + kind + ":" + id
	rows, e := tx.Query(`SELECT object_id,x,y FROM layouts WHERE grouping=?`, group)
	if e != nil {
		http.Error(w, "scope layout lookup failed", 500)
		return
	}
	positions := map[string][2]float64{}
	explicitPositions := map[string]bool{}
	for rows.Next() {
		var object string
		var x, y float64
		if e = rows.Scan(&object, &x, &y); e != nil {
			rows.Close()
			http.Error(w, "scope layout read failed", 500)
			return
		}
		positions[object] = [2]float64{x, y}
		explicitPositions[object] = true
	}
	rows.Close()
	for index, node := range scene.Nodes {
		if _, ok := positions[node.ID]; ok {
			continue
		}
		point := [2]float64{180 + float64(index%5)*240, 160 + float64(index/5)*180}
		if node.X != 0 || node.Y != 0 {
			point = [2]float64{node.X, node.Y}
		}
		positions[node.ID] = point
	}
	if kind == "global" {
		siteIndex, wanIndex, providerIndex, serviceIndex := 0, 0, 0, 0
		for _, node := range scene.Nodes {
			if explicitPositions[node.ID] || node.X != 0 || node.Y != 0 {
				continue
			}
			if node.Kind == "site" {
				positions[node.ID] = [2]float64{180 + float64(siteIndex/5)*280, 280 + float64(siteIndex%5)*190}
				siteIndex++
			} else {
				switch topologyText(node.Data, "service_category") {
				case "internet", "mpls":
					positions[node.ID] = [2]float64{600 + float64(wanIndex/4)*240, 190 + float64(wanIndex%4)*210}
					wanIndex++
				case "cloud_provider":
					positions[node.ID] = [2]float64{980 + float64(providerIndex/4)*250, 110 + float64(providerIndex%4)*170}
					providerIndex++
				default:
					positions[node.ID] = [2]float64{940 + float64(serviceIndex/4)*260, 470 + float64(serviceIndex%4)*180}
					serviceIndex++
				}
			}
		}
	}

	if kind == "site" {
		if !explicitPositions[id] {
			positions[id] = [2]float64{220, 550}
		}
		externalIndex := 0
		for _, node := range scene.Nodes {
			if node.Boundary && node.Kind == "service" && !explicitPositions[node.ID] && node.X == 0 && node.Y == 0 {
				positions[node.ID] = [2]float64{1120, 410 + float64(externalIndex)*220}
				externalIndex++
			}
		}
		index := 0
		for _, node := range scene.Nodes {
			if node.Kind == "building" {
				if !explicitPositions[node.ID] && (node.X == 0 && node.Y == 0) {
					positions[node.ID] = [2]float64{220 + float64(index%3)*500, 160 + float64(index/3)*360}
				}
				index++
			}
		}
		powerCounts := map[string]int{}
		for _, node := range scene.Nodes {
			if node.Kind == "generator" || node.Kind == "ups" {
				parent := s.Parents[node.ID]
				if parentNode, ok := scene.ByID[parent]; ok && parentNode.Kind == "building" {
					if !explicitPositions[node.ID] && node.X == 0 && node.Y == 0 {
						point := positions[parent]
						n := powerCounts[parent]
						positions[node.ID] = [2]float64{point[0] - 100 + float64(n%2)*200, point[1] + 145 + float64(n/2)*115}
						powerCounts[parent]++
					}
				}
			}
		}
	}
	if kind == "building" {
		floorIndex := 0
		roomCounts := map[string]int{}
		for _, node := range scene.Nodes {
			if node.Kind == "floor" {
				if !explicitPositions[node.ID] && node.X == 0 && node.Y == 0 {
					positions[node.ID] = [2]float64{180 + float64(floorIndex%3)*480, 180 + float64(floorIndex/3)*350}
				}
				floorIndex++
			}
		}
		for _, node := range scene.Nodes {
			if node.Parent != "" {
				if !explicitPositions[node.ID] && node.X == 0 && node.Y == 0 {
					point := positions[node.Parent]
					n := roomCounts[node.Parent]
					positions[node.ID] = [2]float64{point[0] + float64(n%2)*190, point[1] + float64(n/2)*145}
					roomCounts[node.Parent]++
				}
			}
		}
	}
	viewNodes, viewEdges := scene.Nodes, scene.Edges
	if r.URL.Query().Has("min_x") || r.URL.Query().Has("max_x") || r.URL.Query().Has("min_y") || r.URL.Query().Has("max_y") {
		bounds := make([]float64, 4)
		for i, key := range []string{"min_x", "min_y", "max_x", "max_y"} {
			value, err := strconv.ParseFloat(r.URL.Query().Get(key), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				http.Error(w, "provide finite viewport min_x,min_y,max_x,max_y", 400)
				return
			}
			bounds[i] = value
		}
		if bounds[0] > bounds[2] || bounds[1] > bounds[3] {
			http.Error(w, "invalid viewport bounds", 400)
			return
		}
		viewNodes = []topologyNode{}
		viewEdges = []topologyEdge{}
		for _, node := range scene.Nodes {
			point := positions[node.ID]
			if point[0] >= bounds[0] && point[0] <= bounds[2] && point[1] >= bounds[1] && point[1] <= bounds[3] {
				viewNodes = append(viewNodes, node)
			}
		}
		for _, edge := range scene.Edges {
			a, b := positions[edge.Source], positions[edge.Target]
			if math.Max(a[0], b[0]) >= bounds[0] && math.Min(a[0], b[0]) <= bounds[2] && math.Max(a[1], b[1]) >= bounds[1] && math.Min(a[1], b[1]) <= bounds[3] {
				viewEdges = append(viewEdges, edge)
			}
		}
	}

	// One cursor spans nodes then edges; every edge page repeats its endpoint
	// summaries so a viewport/page can never contain dangling connections.
	total := len(viewNodes) + len(viewEdges)
	last := offset + limit
	if last > total {
		last = total
	}
	nodes := []topologyNode{}
	edges := []topologyEdge{}
	included := map[string]bool{}
	add := func(n topologyNode) {
		if !included[n.ID] {
			included[n.ID] = true
			nodes = append(nodes, n)
		}
		if n.Parent != "" && !included[n.Parent] {
			included[n.Parent] = true
			nodes = append(nodes, scene.ByID[n.Parent])
		}
	}
	for i := offset; i < last; i++ {
		if i < len(viewNodes) {
			add(viewNodes[i])
		} else {
			edge := viewEdges[i-len(viewNodes)]
			edges = append(edges, edge)
			add(scene.ByID[edge.Source])
			add(scene.ByID[edge.Target])
		}
	}
	layouts := map[string]any{}
	for _, n := range nodes {
		if position, ok := positions[n.ID]; ok {
			layouts[n.ID] = map[string]float64{"x": position[0], "y": position[1]}
		}
	}
	next := ""
	if last < total {
		next = strconv.Itoa(last)
	}
	actions := scene.Actions
	if p.Role == "viewer" {
		actions = []string{}
	}
	var layoutVersion int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM audit WHERE action='layout_update' AND object_id=?`, group).Scan(&layoutVersion); err != nil {
		http.Error(w, "layout version unavailable", 500)
		return
	}
	respond(map[string]any{"scope": scene.Scope, "scope_kind": kind, "breadcrumbs": scene.Path, "nodes": nodes, "edges": edges, "node_count": len(scene.Nodes), "edge_count": len(scene.Edges), "next_cursor": next, "sequence": seq, "allowed_creation_kinds": actions, "positions": layouts, "layout_group": group, "layout_version": layoutVersion})
}

func containsTopologyString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
