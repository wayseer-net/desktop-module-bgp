package bgp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/websocket"
)

// wsStream is a RIS Live WebSocket; it pings so a dead connection shows within readTimeout.
type wsStream struct {
	conn *websocket.Conn
	stop context.CancelFunc
}

type outgoing struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func dialWS(ctx context.Context, u *url.URL, subs []subscription) (*wsStream, error) {
	origin := url.URL{Scheme: "https", Host: u.Host}
	if u.Scheme == "ws" {
		origin.Scheme = "http"
	}
	cfg, err := websocket.NewConfig(u.String(), origin.String())
	if err != nil {
		return nil, err
	}
	cfg.Header = http.Header{"User-Agent": {userAgent}}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := cfg.DialContext(dctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", u.Redacted(), err)
	}
	conn.MaxPayloadBytes = maxMessage
	for _, s := range subs {
		if err := websocket.JSON.Send(conn, outgoing{Type: "ris_subscribe", Data: s}); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("subscribing at %s: %w", u.Redacted(), err)
		}
	}
	sctx, stop := context.WithCancel(ctx)
	context.AfterFunc(sctx, func() { _ = conn.Close() })
	go ping(sctx, conn)
	return &wsStream{conn: conn, stop: stop}, nil
}

// ping asks for a pong every pingEvery until ctx ends.
func ping(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if websocket.JSON.Send(conn, outgoing{Type: "ping"}) != nil {
				return
			}
		}
	}
}

func (s *wsStream) read() ([]byte, error) {
	if err := s.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	var b []byte
	err := websocket.Message.Receive(s.conn, &b)
	return b, err
}

func (s *wsStream) close() { s.stop() }
