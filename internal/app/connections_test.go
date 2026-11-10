package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConnectionSpecifications(t *testing.T) {
	for _, medium := range []string{"fiber", "copper_twisted_pair", "coax", "twinax_dac", "wireless_radio", "backplane", "other"} {
		t.Run(medium, func(t *testing.T) {
			spec := map[string]any{"physical_type": medium, "cable_type": "test", "connector_a": "A", "connector_b": "B", "custom_type": "Serial"}
			if _, err := validateLinkSpec(spec); err != nil {
				t.Fatal(err)
			}
		})
	}
	for cls, types := range map[string][]string{
		"virtual":          {"ethernet", "vlan", "bridge", "veth", "port_group", "overlay", "bond", "lag", "loopback", "other"},
		"logical_tunnel":   {"ipsec", "wireguard", "gre", "vxlan", "l2tp", "geneve", "ipip", "sit", "openvpn", "ssl_vpn", "other"},
		"provider_service": {"pls", "leased_circuit", "dark_fiber", "wavelength", "carrier_ethernet", "mpls", "internet", "evpn", "sd_wan", "other"},
	} {
		key := map[string]string{"virtual": "virtual_type", "logical_tunnel": "tunnel_protocol", "provider_service": "service_type"}[cls]
		for _, kind := range types {
			t.Run(cls+"/"+kind, func(t *testing.T) {
				clean, err := validateLinkSpec(map[string]any{"connection_class": cls, key: kind, "custom_type": "Custom", "label": " primary ", "description": " test "})
				if err != nil {
					t.Fatal(err)
				}
				if clean["label"] != "primary" || clean["description"] != "test" {
					t.Fatal(clean)
				}
			})
		}
	}
	for _, spec := range []map[string]any{
		{"connection_class": "virtual", "virtual_type": "other"},
		{"connection_class": "virtual", "virtual_type": "vlan", "vlan_id": 4095},
		{"connection_class": "virtual", "virtual_type": "overlay", "vni": 1.5},
		{"connection_class": "virtual", "virtual_type": "overlay", "vni": 0},
		{"connection_class": "virtual", "virtual_type": "invalid"},
		{"connection_class": "physical", "physical_type": "backplane", "length_m": "NaN"},
		{"connection_class": "physical", "physical_type": "backplane", "length_m": "Inf"},
	} {
		if _, err := validateLinkSpec(spec); err == nil {
			t.Fatalf("accepted invalid specification: %v", spec)
		}
	}
}

func TestPhysicalAndVirtualConnectionsShareInterfaces(t *testing.T) {
	a, db := editorApp(t, "editor")
	// Run the actual migration over a legacy store, preserving its cable and route.
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d1','device','one','{}',0,0),('d2','device','two','{}',0,0),('duct','conduit','duct','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p1','d1','one','{}'),('p2','d2','two','{}'),('p3','d1','three','{}'),('p4','d1','four','{}')`,
		`INSERT INTO links VALUES('cable','p1','p2','{"medium":"fiber"}')`,
		`INSERT INTO cable_routes VALUES('route','cable',0,'duct')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	specs := []string{
		`{"connection_class":"virtual","virtual_type":"vlan","vlan_id":10}`,
		`{"connection_class":"virtual","virtual_type":"vlan","vlan_id":20}`,
		`{"connection_class":"logical_tunnel","tunnel_protocol":"wireguard"}`,
		`{"connection_class":"provider_service","service_type":"mpls"}`,
	}
	for i, spec := range specs {
		body := fmt.Sprintf(`{"id":"v%d","source_interface":"p1","target_interface":"p2","data":%s}`, i, spec)
		if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 204 {
			t.Fatalf("%d: %d %s", i, w.Code, w.Body.String())
		}
	}
	// A virtual switch may connect interfaces on the same host.
	body := `{"id":"local","source_interface":"p3","target_interface":"p4","data":{"connection_class":"virtual","virtual_type":"bridge"}}`
	if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	body = `{"source_interface":"p2","target_interface":"p1","data":{"physical_type":"backplane"}}`
	if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/trace?interface_id=p1", "/api/trace?interface_id=p1&link_id=v0"} {
		w := apiRequest(a, http.MethodGet, path, "", "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got struct {
			Segments []struct {
				LinkID string `json:"link_id"`
				Route  []any  `json:"route"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := "cable"
		if strings.Contains(path, "link_id=") {
			want = "v0"
		}
		if len(got.Segments) != 1 || got.Segments[0].LinkID != want {
			t.Fatalf("wrong trace: %s", w.Body.String())
		}
		if want == "cable" && len(got.Segments[0].Route) != 1 {
			t.Fatal("migration lost route")
		}
	}
	if w := apiRequest(a, http.MethodPost, "/api/routes", `{"link_id":"v0","infrastructure_ids":["duct"]}`, "s"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Removing the physical cable frees the port without disturbing virtual links.
	if w := apiRequest(a, http.MethodDelete, "/api/links?id=cable", "", "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := apiRequest(a, http.MethodPost, "/api/links", body, "s"); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLinkUpdate(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d1','device','one','{}',0,0),('d2','device','two','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p1','d1','one','{}'),('p2','d2','two','{}')`,
		`INSERT INTO interfaces VALUES('p3','d2','three','{}')`,
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('d3','device','three','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p4','d1','four','{}'),('p5','d3','five','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	fiber := func(label string) string {
		return `{"connection_class":"physical","physical_type":"fiber","cable_type":"OS2","connector_a":"LC","connector_b":"LC","label":"` + label + `"}`
	}
	post := func(body string) int {
		return apiRequest(a, http.MethodPost, "/api/links", body, "s").Code
	}
	put := func(body, sid string) (int, string) {
		w := apiRequest(a, http.MethodPut, "/api/links", body, sid)
		return w.Code, w.Body.String()
	}
	if code := post(`{"id":"c1","source_interface":"p1","target_interface":"p2","data":` + fiber("uplink") + `}`); code != 204 {
		t.Fatalf("create status=%d", code)
	}
	if _, err := db.Exec(`INSERT INTO cable_routes(id,link_id,sequence,infrastructure_id) VALUES('r1','c1',0,'m1')`); err != nil {
		t.Fatal(err)
	}
	// Unchanged resubmit must not conflict with itself.
	if code, body := put(`{"id":"c1","source_interface":"p1","target_interface":"p2","data":`+fiber("uplink")+`}`, "s"); code != 204 {
		t.Fatalf("identical update status=%d %s", code, body)
	}
	// Spec-only edit.
	if code, body := put(`{"id":"c1","source_interface":"p1","target_interface":"p2","data":`+fiber("core")+`}`, "s"); code != 204 {
		t.Fatalf("spec update status=%d %s", code, body)
	}
	var stored string
	if err := db.QueryRow(`SELECT data FROM links WHERE id='c1'`).Scan(&stored); err != nil || !strings.Contains(stored, `"label":"core"`) {
		t.Fatalf("spec not stored: %s %v", stored, err)
	}
	// Endpoint move to a free interface.
	if code, body := put(`{"id":"c1","source_interface":"p1","target_interface":"p3","data":`+fiber("core")+`}`, "s"); code != 204 {
		t.Fatalf("move update status=%d %s", code, body)
	}
	// Conflicting move: p4 already peers d3 through c2.
	if code := post(`{"id":"c2","source_interface":"p4","target_interface":"p5","data":` + fiber("other") + `}`); code != 204 {
		t.Fatalf("second link status=%d", code)
	}
	if code, _ := put(`{"id":"c1","source_interface":"p4","target_interface":"p3","data":`+fiber("core")+`}`, "s"); code != 409 {
		t.Fatalf("conflicting move status=%d", code)
	}
	if code, _ := put(`{"id":"missing","source_interface":"p1","target_interface":"p3","data":`+fiber("x")+`}`, "s"); code != 404 {
		t.Fatalf("missing status=%d", code)
	}
	if code, _ := put(`{"id":"c1","source_interface":"p1","target_interface":"p3","data":{"connection_class":"physical"}}`, "s"); code != 400 {
		t.Fatalf("bad spec status=%d", code)
	}
	if code, _ := put(`{"id":"c1","source_interface":"p1","target_interface":"p1","data":`+fiber("x")+`}`, "s"); code != 400 {
		t.Fatalf("same interface status=%d", code)
	}
	a.sessions["v"] = principal{User: "vic", Role: "viewer"}
	a.sessionExpires["v"] = time.Now().Add(time.Hour)
	if code, _ := put(`{"id":"c1","source_interface":"p1","target_interface":"p3","data":`+fiber("x")+`}`, "v"); code != 403 {
		t.Fatalf("viewer status=%d", code)
	}
	var routes int
	if err := db.QueryRow(`SELECT count(*) FROM cable_routes WHERE link_id='c1'`).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("routes preserved=%d err=%v", routes, err)
	}
	var audits, events int
	if err := db.QueryRow(`SELECT count(*) FROM audit WHERE action='link_update'`).Scan(&audits); err != nil || audits == 0 {
		t.Fatalf("link_update audit=%d err=%v", audits, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM events WHERE payload LIKE '%link_update%'`).Scan(&events); err != nil || events == 0 {
		t.Fatalf("link_update events=%d err=%v", events, err)
	}
}

func TestCSVVirtualConnectionsAndRoutes(t *testing.T) {
	a, db := editorApp(t, "editor")
	if err := migrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO objects(id,kind,name,data,x,y) VALUES('vm','virtual_machine','vm','{}',0,0),('host','device','host','{}',0,0),('duct','conduit','duct','{}',0,0)`,
		`INSERT INTO interfaces VALUES('p','host','port','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	csv := "kind,id,name,device_id,source_interface,target_interface,connection_class,virtual_type,vlan_id,custom_type,link_id,infrastructure_id\n" +
		"interface,vi,eth0,vm,,,,,,,,\n" +
		"connection,v1,,,vi,p,virtual,vlan,42,,,\n" +
		"connection,v2,,,vi,p,virtual,other,,Custom virtual,,\n"
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		w := apiRequest(a, http.MethodPost, path, csv, "s")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM links WHERE id='v1'`).Scan(&raw); err != nil || !strings.Contains(raw, `"vlan_id":42`) {
		t.Fatal(raw, err)
	}
	route := "kind,id,link_id,infrastructure_id\nroute,r,v1,duct\n"
	for _, path := range []string{"/api/import?dry_run=1", "/api/import"} {
		w := apiRequest(a, http.MethodPost, path, route, "s")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
