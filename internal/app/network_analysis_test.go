package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestQualityAddressScopesAndDeletedVRFs(t *testing.T) {
	ports := []analysisPort{
		{ID: "a", Network: "red", VRFs: []string{"prod"}, IP: "192.0.2.1/24"},
		{ID: "b", Network: "red", VRFs: []string{"prod"}, IP: "::ffff:192.0.2.1"},
		{ID: "c", Network: "blue", VRFs: []string{"prod"}, IP: "192.0.2.1"},
		{ID: "d", Network: "red", VRFs: []string{"guest"}, IP: "192.0.2.1"},
		{ID: "e", MissingVRFs: []string{"deleted"}, IP: "bad address"},
	}
	issues := qualityIssues(ports)
	kinds := map[string]int{}
	for _, issue := range issues {
		kinds[issue.Kind]++
		if issue.Kind == "duplicate_ip" && len(issue.Ports) != 2 {
			t.Fatal(issue)
		}
	}
	if kinds["address_reuse"] != 1 || kinds["duplicate_ip"] != 1 || kinds["deleted_vrf"] != 1 || kinds["invalid_ip"] != 1 {
		t.Fatal(issues)
	}
}
func TestQualityAPIReadsLegacyAssignmentsAndPermissions(t *testing.T) {
	a := vrfFixture(t)
	for _, v := range []struct{ id, device, ip string }{{"qa", "router", "2001:db8::1/64"}, {"qb", "switch", "2001:db8::1"}} {
		w := apiRequest(a, http.MethodPost, "/api/interfaces", `{"id":"`+v.id+`","device_id":"`+v.device+`","name":"`+v.id+`","data":{"speed":"1G","ip_address":"`+v.ip+`","vrfs":["Production"]}}`, "s")
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := a.db.Exec(`UPDATE interfaces SET data='{"vrfs":["Removed"]}' WHERE id='qa'`); err != nil {
		t.Fatal(err)
	}
	w := apiRequest(a, http.MethodGet, "/api/topology/quality", "", "s")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Issues []qualityIssue `json:"issues"`
		Total  int            `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Issues[0].Kind != "deleted_vrf" {
		t.Fatal(w.Body.String())
	}
	for _, req := range []struct {
		method, path, session string
		code                  int
	}{{"POST", "/api/topology/quality", "s", 405}, {"GET", "/api/topology/quality?offset=-1", "s", 400}, {"GET", "/api/topology/quality", "", 401}} {
		w = apiRequest(a, req.method, req.path, "", req.session)
		if w.Code != req.code {
			t.Fatal(req, w.Code, w.Body.String())
		}
	}
}
