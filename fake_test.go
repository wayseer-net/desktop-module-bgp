package bgp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"
)

// risLive is a fake RIS Live: it sends its lines to each client that subscribes, answers
// pings, and records what clients sent.
type risLive struct {
	mu      sync.Mutex
	lines   []string
	subs    []string // each ris_subscribe's data
	clients []string // each connection's client identifier
	drop    bool     // close each connection once its lines are sent
}

// serveRIS starts a fake RIS Live sending lines over WebSocket; its URL is ws://.
func serveRIS(t *testing.T, lines ...string) (*risLive, string) {
	t.Helper()
	ris := &risLive{lines: lines}
	srv := httptest.NewServer(websocket.Handler(ris.serve))
	t.Cleanup(srv.Close)
	return ris, "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws/"
}

// fixtureLines are testdata/stream.ndjson as strings.
func fixtureLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, l := range fixture(t) {
		out = append(out, string(l))
	}
	return out
}

func (r *risLive) serve(ws *websocket.Conn) {
	defer func() { _ = ws.Close() }()
	r.mu.Lock()
	r.clients = append(r.clients, ws.Request().URL.Query().Get("client"))
	lines, drop := r.lines, r.drop
	r.mu.Unlock()
	sent := false
	for {
		var in struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := websocket.JSON.Receive(ws, &in); err != nil {
			return
		}
		switch in.Type {
		case "ping":
			_ = websocket.Message.Send(ws, `{"type":"pong","data":null}`)
		case "ris_subscribe":
			r.mu.Lock()
			r.subs = append(r.subs, string(in.Data))
			r.mu.Unlock()
		}
		if !sent {
			sent = true
			for _, l := range lines {
				_ = websocket.Message.Send(ws, l)
			}
			if drop {
				return
			}
		}
	}
}

func (r *risLive) seen() (subs, clients []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.subs...), append([]string(nil), r.clients...)
}

// serveStream serves testdata as a static directory, as the marketplace's sandbox serves a
// fixture; its URL is the stream file's.
func serveStream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)
	return srv.URL + "/stream.ndjson"
}
