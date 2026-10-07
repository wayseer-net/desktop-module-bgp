package bgp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"wayseer.dev/sdk"
	"wayseer.dev/sdk/sdktest"
)

// closedURL is a server's URL that refuses connections.
func closedURL(scheme string) string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return scheme + strings.TrimPrefix(srv.URL, "http")
}

// configured is a module configured with options.
func configured(t *testing.T, options string) *Module {
	t.Helper()
	cfg, err := sdktest.Config("bgp", options)
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConformance(t *testing.T) {
	_, url := serveRIS(t, fixtureLines(t)...)
	sdktest.Conform(t, sdktest.Case{
		New:      func() sdk.Module { return New() },
		Name:     "bgp",
		Options:  "url: " + url + "\ninterval: 1s\n" + fixtureOptions,
		Observe:  1500 * time.Millisecond, // past the first delta, so the store checks the stream's world
		Failing:  "url: " + closedURL("ws") + "\n" + fixtureOptions,
		Manifest: "manifest.yaml",
	})
}

// TestConformanceOverTheHTTPStream runs as the marketplace does: the fixture served as a file.
func TestConformanceOverTheHTTPStream(t *testing.T) {
	sdktest.Conform(t, sdktest.Case{
		New:      func() sdk.Module { return New() },
		Name:     "bgp",
		Options:  "url: " + serveStream(t) + "\ninterval: 1s\n" + fixtureOptions,
		Observe:  1500 * time.Millisecond,
		Failing:  "url: " + closedURL("http") + "\n" + fixtureOptions,
		Manifest: "manifest.yaml",
	})
}

func TestItSubscribesWithItsFiltersAndClientID(t *testing.T) {
	ris, url := serveRIS(t)
	m := configured(t, "url: "+url+"\nprefixes: [203.0.113.0/24]\nhosts: [rrc00]\ntypes: [UPDATE]")
	sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
	sdktest.Eventually(t, func() bool { subs, _ := ris.seen(); return len(subs) == 1 })
	subs, clients := ris.seen()
	if subs[0] != `{"host":"rrc00","type":"UPDATE","prefix":["203.0.113.0/24"],"moreSpecific":true,"lessSpecific":false}` {
		t.Errorf("subscribed with %s", subs[0])
	}
	if clients[0] != "wayseer-bgp" {
		t.Errorf("client %q", clients[0])
	}
}

func TestTheStreamReachesTheSink(t *testing.T) {
	for name, url := range map[string]string{"websocket": "", "http": serveStream(t)} {
		t.Run(name, func(t *testing.T) {
			if url == "" {
				_, url = serveRIS(t, fixtureLines(t)...)
			}
			m := configured(t, "url: "+url+"\ninterval: 1s\n"+fixtureOptions)
			sink := sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
			sdktest.Eventually(t, func() bool { return hasEvent(sink.Events(), "possible hijack") })
			if h := m.Health(); h.Err != nil {
				t.Errorf("health %+v", h)
			}
		})
	}
}

func hasEvent(evs []sdk.Event, words string) bool {
	return slices.ContainsFunc(evs, func(e sdk.Event) bool { return strings.Contains(e.Message, words) })
}

func TestItReconnectsWhenTheConnectionDrops(t *testing.T) {
	quickRetries(t)
	ris, url := serveRIS(t, fixtureLines(t)[1])
	ris.drop = true
	m := configured(t, "url: "+url+"\nprefixes: [203.0.113.0/24]")
	sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
	sdktest.Eventually(t, func() bool { _, clients := ris.seen(); return len(clients) >= 3 })
}

func TestARISErrorShowsInHealth(t *testing.T) {
	quickRetries(t)
	ris, url := serveRIS(t, `{"type":"ris_error","data":{"message":"client too slow"}}`)
	ris.drop = true
	m := configured(t, "url: "+url+"\nprefixes: [203.0.113.0/24]")
	sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
	sdktest.Eventually(t, func() bool {
		h := m.Health()
		return h.Err != nil && strings.Contains(h.Err.Error(), "client too slow")
	})
}

func TestAnUnreadableMessageIsANote(t *testing.T) {
	_, url := serveRIS(t, `{"type":"ris_message","data":{"type":"UPDATE","peer":"nope"}}`)
	m := configured(t, "url: "+url+"\nprefixes: [203.0.113.0/24]")
	sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
	sdktest.Eventually(t, func() bool { return strings.Contains(m.Health().Note, "1 message") })
	if m.Health().Err != nil {
		t.Errorf("health %+v", m.Health())
	}
}

func TestTheHTTPStreamSendsItsFilterInAHeader(t *testing.T) {
	var mu sync.Mutex
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.URL.Query().Get("client")+" "+r.Header.Get("X-RIS-Subscribe"))
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	m := configured(t, "url: "+srv.URL+"/v1/stream/\nprefixes: [203.0.113.0/24]\ntypes: [UPDATE]\nclient: lab-1")
	sdktest.Run(t, func(ctx context.Context, s *sdktest.Sink) error { return m.Run(ctx, s) })
	sdktest.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(headers) > 0 })
	mu.Lock()
	defer mu.Unlock()
	if headers[0] != `lab-1 {"type":"UPDATE","prefix":["203.0.113.0/24"],"moreSpecific":true,"lessSpecific":false}` {
		t.Errorf("request %s", headers[0])
	}
}

func TestBackoffDoublesToAMinute(t *testing.T) {
	var got []time.Duration
	for attempt := range 9 {
		got = append(got, backoff(attempt))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("backoff %v", got)
		}
	}
}

// quickRetries makes reconnecting take milliseconds for the test.
func quickRetries(t *testing.T) {
	old := retryBase
	retryBase = 10 * time.Millisecond
	t.Cleanup(func() { retryBase = old })
}
