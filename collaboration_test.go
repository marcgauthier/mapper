package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcgauthier/websocket"
)

func TestWebSocketBroadcastsCommittedObjectToConnectedEditors(t *testing.T) {
	db := scaleDB(t)
	const editors = 100
	sessions, expires := make(map[string]principal, editors), make(map[string]time.Time, editors)
	for i := 0; i < editors; i++ {
		sid := fmt.Sprintf("editor-%d", i)
		sessions[sid] = principal{User: sid, Role: "editor"}
		expires[sid] = time.Now().Add(time.Hour)
	}
	a := &App{db: db, sessions: sessions, sessionExpires: expires, clients: map[chan []byte]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", a.ws)
	mux.HandleFunc("/api/", a.api)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	clients := make([]*websocket.Conn, 0, editors)
	for i := 0; i < editors; i++ {
		sid := fmt.Sprintf("editor-%d", i)
		h := http.Header{"Origin": []string{srv.URL}, "Cookie": []string{"mapper_session=" + sid}}
		c, _, e := (&websocket.Dialer{Subprotocols: []string{zstdWebSocketProtocol}}).Dial(wsURL, h)
		if e != nil {
			t.Fatal(e)
		}
		clients = append(clients, c)
		defer c.Close()
	}
	r := httptest.NewRequest(http.MethodPost, "/api/objects", strings.NewReader(`{"id":"shared","kind":"device","building_id":"B1","name":"core-1","x":3,"y":7}`))
	r.AddCookie(&http.Cookie{Name: "mapper_session", Value: "editor-0"})
	w := httptest.NewRecorder()
	a.api(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("write status=%d: %s", w.Code, w.Body.String())
	}
	for i, c := range clients {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		kind, frame, e := c.ReadMessage()
		if e != nil {
			t.Fatalf("client %d missed event: %v", i, e)
		}
		if kind != websocket.BinaryMessage {
			t.Fatal("expected Zstandard binary frame")
		}
		msg, e := decodeZstdMessage(frame)
		if e != nil {
			t.Fatal(e)
		}
		var event struct {
			Seq     int64 `json:"seq"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if e = json.Unmarshal(msg, &event); e != nil {
			t.Fatal(e)
		}
		if event.Seq != 1 || event.Payload.ID != "shared" {
			t.Fatalf("client %d got event %#v", i, event)
		}
	}
}
