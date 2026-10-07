package bgp

import (
	"context"
	"errors"
	"io"
	"time"

	"wayseer.dev/sdk"
)

// listen reads RIS Live until ctx ends, reconnecting with backoff; a stream that lasted a
// minute starts the backoff again.
func (m *Module) listen(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		m.health.Store(lost(err))
		if time.Since(started) >= retryMax {
			attempt = 0
		}
		if !sleep(ctx, jitter(backoff(attempt))) {
			return
		}
		attempt++
	}
}

// lost is the health after a stream ends: disconnected when RIS Live ended it, else an error.
func lost(err error) *sdk.Health {
	if errors.Is(err, io.EOF) {
		return &sdk.Health{Disconnected: true}
	}
	return &sdk.Health{Err: err}
}

// session connects, subscribes and applies messages until the stream ends.
func (m *Module) session(ctx context.Context) error {
	m.mu.Lock()
	raw, client, subs := m.opts.URL, m.opts.Client, m.subs
	m.mu.Unlock()
	s, err := dial(ctx, raw, client, subs)
	if err != nil {
		return err
	}
	defer s.close()
	m.health.Store(&sdk.Health{})
	var last risError
	for {
		line, err := s.read()
		if err != nil {
			return ended(err, last)
		}
		if e := m.handle(line); e != "" {
			last = e
			m.health.Store(&sdk.Health{Err: last})
		}
	}
}

// handle applies one message, and returns RIS Live's error if it is one.
func (m *Module) handle(line []byte) risError {
	r, err := parse(line)
	switch {
	case err != nil:
		m.dropped.Add(1)
	case r.kind == kindError:
		return risError(r.err)
	case r.kind == kindMessage:
		m.mu.Lock()
		m.state.apply(&r.msg, time.Now())
		m.mu.Unlock()
	}
	return ""
}
