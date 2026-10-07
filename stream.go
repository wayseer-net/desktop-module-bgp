package bgp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"time"
)

// Connection limits and pacing.
const (
	maxMessage  = 1 << 20 // the largest message read
	dialTimeout = 15 * time.Second
	pingEvery   = 30 * time.Second
	readTimeout = 2*pingEvery + 10*time.Second // a quiet subscription still has pongs
	retryMax    = time.Minute
	userAgent   = "wayseer-bgp/" + version
)

// retryBase is the first wait before reconnecting; it doubles to retryMax.
var retryBase = time.Second

// stream is one subscribed connection to RIS Live.
type stream interface {
	read() ([]byte, error) // the next message; io.EOF when RIS Live ended the stream
	close()
}

// dial connects to RIS Live and subscribes: over WebSocket for ws:// and wss://, else over
// its HTTP stream, one request per subscription.
func dial(ctx context.Context, raw, client string, subs []subscription) (stream, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("client", client)
	u.RawQuery = q.Encode()
	if u.Scheme == "ws" || u.Scheme == "wss" {
		return dialWS(ctx, u, subs)
	}
	return dialHTTP(ctx, u, subs)
}

// backoff is how long to wait before the given reconnect attempt, from 0.
func backoff(attempt int) time.Duration {
	if attempt >= 16 {
		return retryMax
	}
	return min(retryBase<<attempt, retryMax)
}

// jitter spreads reconnects over the last quarter of the wait.
func jitter(d time.Duration) time.Duration {
	return d - time.Duration(rand.Int64N(int64(d/4)+1))
}

// sleep waits d, or less if ctx ends; it reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// risError is a ris_error RIS Live sent.
type risError string

func (e risError) Error() string { return "RIS Live: " + string(e) }

// ended says why a stream stopped, preferring the last ris_error to the read's own error.
func ended(err error, last risError) error {
	switch {
	case last != "":
		return last
	case errors.Is(err, io.EOF):
		return err
	}
	return fmt.Errorf("reading RIS Live: %w", err)
}
