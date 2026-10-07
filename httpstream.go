package bgp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// httpStream reads RIS Live's HTTP stream, a JSON message per line, one request per filter.
type httpStream struct {
	ctx    context.Context
	lines  chan []byte
	errs   chan error
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var streamClient = &http.Client{Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: dialTimeout,
}}

func dialHTTP(ctx context.Context, u *url.URL, subs []subscription) (*httpStream, error) {
	sctx, cancel := context.WithCancel(ctx)
	h := &httpStream{ctx: sctx, lines: make(chan []byte, 64), errs: make(chan error, len(subs)), cancel: cancel}
	for _, s := range subs {
		body, err := open(sctx, u, s)
		if err != nil {
			h.close()
			return nil, err
		}
		h.wg.Go(func() { h.pump(sctx, body) })
	}
	return h, nil
}

// open starts one filtered stream; RIS Live reads the filter from X-RIS-Subscribe.
func open(ctx context.Context, u *url.URL, s subscription) (io.ReadCloser, error) {
	filter, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-RIS-Subscribe", string(filter))
	req.Header.Set("User-Agent", userAgent)
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", u.Redacted(), resp.Status)
	}
	return resp.Body, nil
}

// pump sends each line of body, then why it ended: io.EOF when the server ended it.
func (h *httpStream) pump(ctx context.Context, body io.ReadCloser) {
	defer func() { _ = body.Close() }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxMessage)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		select {
		case h.lines <- append([]byte(nil), sc.Bytes()...):
		case <-ctx.Done():
			return
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	h.errs <- err
}

// read prefers lines, so a stream's lines all come before its end.
func (h *httpStream) read() ([]byte, error) {
	select {
	case l := <-h.lines:
		return l, nil
	default:
	}
	select {
	case l := <-h.lines:
		return l, nil
	case err := <-h.errs:
		return nil, err
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
}

func (h *httpStream) close() {
	h.cancel()
	h.wg.Wait()
}
