package server

// End to end on loopback: a fake relay Durable Object sends a v2 sign-in
// through cloud.Relay to RemoteHandler, as the 2.0+ app does from outside.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sentinel-linux/internal/cloud"
)

func TestRelayToRemoteHandlerSignIn(t *testing.T) {
	s := newCompanionServer(t)
	v2Token(t, s, "op", "Operator") // the user (and its password)
	origin := httptest.NewServer(s.RemoteHandler())
	t.Cleanup(origin.Close)

	conns := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCloudToken {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		ws, err := up.Upgrade(w, r, nil)
		if err == nil {
			conns <- ws
		}
	}))
	t.Cleanup(do.Close)

	r := &cloud.Relay{URL: "ws" + strings.TrimPrefix(do.URL, "http") + "/v1/relay", Token: testCloudToken, Version: "test", Origin: origin.URL}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	var ws *websocket.Conn
	select {
	case ws = <-conns:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never connected")
	}
	send := func(typ byte, id uint32, payload []byte) {
		if err := ws.WriteMessage(websocket.BinaryMessage, cloud.EncodeFrame(typ, id, payload)); err != nil {
			t.Fatal(err)
		}
	}
	next := func() cloud.Frame {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		f, err := cloud.DecodeFrame(data)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	body := []byte(`{"grantType":"password","name":"op","password":"correct horse battery","client":"relay test"}`)
	head, _ := json.Marshal(map[string]any{"m": "POST", "p": "/api/v2/auth/token", "body": true, "h": [][2]string{
		{"content-type", "application/json"}, {"host", "k7f3q9x2ab-cloud.example"}, {"x-forwarded-host", "k7f3q9x2ab-cloud.example"},
		{"x-forwarded-proto", "https"}, {"x-forwarded-for", "203.0.113.50"}, {"origin", "https://k7f3q9x2ab-cloud.example"},
	}})
	send(cloud.FrameReq, 1, head)
	send(cloud.FrameData, 1, body)
	send(cloud.FrameEnd, 1, nil)

	f := next()
	var res struct {
		S    int         `json:"s"`
		H    [][2]string `json:"h"`
		Body bool        `json:"body"`
	}
	if f.Type != cloud.FrameRes || json.Unmarshal(f.Payload, &res) != nil || res.S != 200 || !res.Body {
		t.Fatalf("RES type %d %s", f.Type, f.Payload)
	}
	var got []byte
	for {
		f := next()
		if f.Type == cloud.FrameEnd {
			break
		}
		if f.Type != cloud.FrameData {
			t.Fatalf("frame type %d", f.Type)
		}
		got = append(got, f.Payload...)
	}
	var tok map[string]any
	if json.Unmarshal(got, &tok) != nil || !strings.HasPrefix(tok["accessToken"].(string), "sat_") {
		t.Fatalf("sign-in through the relay: %s", got)
	}
	entries, _ := s.Store.AuditLog(context.Background(), 20)
	for _, e := range entries {
		if e.Action == "Signed in" && strings.Contains(e.Detail, "203.0.113.50") {
			return
		}
	}
	t.Fatalf("sign-in not audited with the client's address: %+v", entries)
}
