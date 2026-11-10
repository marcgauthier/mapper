package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marcgauthier/websocket"
)

func TestTopologyWebSocket1000Users(t *testing.T) {
	if testing.Short() {
		t.Skip("1000 connected users")
	}
	a := topologyFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", a.ws)
	server := httptest.NewServer(mux)
	defer server.Close()
	clients := []*websocket.Conn{}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for i := 0; i < 1000; i++ {
		sid := fmt.Sprintf("topology-user-%d", i)
		a.mu.Lock()
		a.sessions[sid] = principal{User: sid, Role: "editor"}
		a.sessionExpires[sid] = time.Now().Add(time.Hour)
		a.mu.Unlock()
		c, _, e := (&websocket.Dialer{Subprotocols: []string{zstdWebSocketProtocol}}).Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", http.Header{"Origin": []string{server.URL}, "Cookie": []string{"mapper_session=" + sid}})
		if e != nil {
			t.Fatal(i, e)
		}
		clients = append(clients, c)
	}
	start := time.Now()
	topologyPost(t, a, `{"id":"generator-live","kind":"generator","name":"Live generator","building_id":"b1"}`)
	latencies := []time.Duration{}
	for i, c := range clients {
		_ = c.SetReadDeadline(start.Add(5 * time.Second))
		kind, frame, e := c.ReadMessage()
		if e != nil || kind != websocket.BinaryMessage {
			t.Fatal(i, kind, e)
		}
		raw, e := decodeZstdMessage(frame)
		if e != nil {
			t.Fatal(e)
		}
		var event struct {
			Seq     int64          `json:"seq"`
			Payload map[string]any `json:"payload"`
		}
		if e = json.Unmarshal(raw, &event); e != nil || event.Seq == 0 || event.Payload["id"] != "generator-live" {
			t.Fatal(string(raw), e)
		}
		latencies = append(latencies, time.Since(start))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("1000-user local commit-request-to-read p95=%s max=%s", latencies[949], latencies[999])
	if latencies[949] > 2*time.Second {
		t.Fatal("local p95 exceeds 2s")
	}
}
func BenchmarkFocusedTopology100K(b *testing.B) { benchmarkFocusedTopology100K(b, false) }
func BenchmarkVRFTopology100K(b *testing.B)     { benchmarkFocusedTopology100K(b, true) }
func benchmarkFocusedTopology100K(b *testing.B, withVRFs bool) {
	db := scaleDB(b)
	if e := migrateSchema(db); e != nil {
		b.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		b.Fatal(e)
	}
	stmt, e := tx.Prepare(`INSERT INTO objects(id,kind,name,data,x,y) VALUES(?,?,?,?,0,0)`)
	if e != nil {
		b.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		site := fmt.Sprintf("s%03d", i)
		building := fmt.Sprintf("b%03d", i)
		for _, o := range []struct{ id, kind, raw string }{{site, "site", "{}"}, {building, "building", fmt.Sprintf(`{"site_id":%q}`, site)}, {"f" + building, "floor", fmt.Sprintf(`{"building_id":%q,"site_id":%q}`, building, site)}} {
			if _, e = stmt.Exec(o.id, o.kind, o.id, o.raw); e != nil {
				b.Fatal(e)
			}
		}
		for j := 0; j < 1000; j++ {
			id := fmt.Sprintf("device-%03d-%04d", i, j)
			raw := fmt.Sprintf(`{"site_id":%q,"building_id":%q,"floor_id":%q}`, site, building, "f"+building)
			if withVRFs {
				raw = strings.TrimSuffix(raw, "}") + `,"device_type":"router","vrfs":["Management","Production"]}`
			}
			if _, e = stmt.Exec(id, "device", id, raw); e != nil {
				b.Fatal(e)
			}
		}
	}
	stmt.Close()
	for _, q := range []string{
		`INSERT INTO interfaces(id,device_id,name,data) SELECT 'a:'||id,id,'A','{}' FROM objects WHERE kind='device'`,
		`INSERT INTO interfaces(id,device_id,name,data) SELECT 'z:'||id,id,'Z','{}' FROM objects WHERE kind='device'`,
		`INSERT INTO links(id,source_interface,target_interface,data) SELECT 'link:'||a.id,'a:'||a.id,'z:'||b.id,'{"connection_class":"physical","medium":"backplane"}' FROM objects a JOIN objects b ON b.id=printf('device-%03d-%04d',CAST(substr(a.id,8,3) AS INTEGER),((CAST(substr(a.id,12,4) AS INTEGER)+1)%1000)) WHERE a.kind='device'`,
	} {
		if _, e = tx.Exec(q); e != nil {
			b.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		b.Fatal(e)
	}
	a := &App{db: db}
	query := "/api/topology/view?scope_kind=global&limit=250"
	if withVRFs {
		query += "&vrf=Management"
	}
	r := httptest.NewRequest(http.MethodGet, query, nil)
	coldStart := time.Now()
	if w := httptest.NewRecorder(); true {
		a.topologyView(w, r, db, principal{User: "bench", Role: "viewer"})
		if w.Code != 200 {
			b.Fatal(w.Code, w.Body.String())
		}
	}
	cold := time.Since(coldStart)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		a.topologyView(w, r, db, principal{User: "bench", Role: "viewer"})
		if w.Code != 200 {
			b.Fatal(w.Code, w.Body.String())
		}
		var page topologyTestPage
		if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil || len(page.Nodes) != 100 {
			b.Fatal(len(page.Nodes), e)
		}
	}
	b.ReportMetric(float64(cold.Microseconds())/1000, "cold-ms")
}
