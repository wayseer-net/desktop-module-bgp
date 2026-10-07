package bgp

import (
	"fmt"
	"slices"
	"time"

	"wayseer.dev/sdk"
)

// flush closes one interval: it raises the interval's events, ages and bounds the working
// set, records series points, and returns the events to send.
func (s *state) flush(now time.Time) []sdk.Event {
	s.summarise(now)
	s.trimPrefixes()
	s.keepInUse(now)
	s.age(now.Add(-s.opts.StaleAfter))
	s.trim()
	s.record(now)
	evs := s.pending
	if s.dropped > 0 {
		evs = append(evs, sdk.Event{
			ID: fmt.Sprintf("dropped/%d", now.UnixNano()), At: now, Severity: sdk.SevWarn, Kind: "dropped", Source: s.src,
			Message: fmt.Sprintf("%d more events since the last send were left out", s.dropped),
		})
	}
	s.pending, s.dropped = nil, 0
	return evs
}

// keepInUse marks seen whatever a route still uses, so a quiet but carried route stays whole.
func (s *state) keepInUse(now time.Time) {
	for _, ps := range s.prefixes {
		for key, path := range ps.routes {
			if p := s.peers[key]; p != nil {
				p.seen = now
				s.ases[p.asn] = now
				if c := s.collectors[p.collector]; c != nil {
					c.seen = now
				}
			}
			s.touchPath(path, now)
		}
	}
}

// age forgets what hasn't been seen since cutoff; watched prefixes stay.
func (s *state) age(cutoff time.Time) {
	stale := func(t time.Time) bool { return t.Before(cutoff) }
	for p, ps := range s.prefixes {
		if !ps.watched && len(ps.routes) == 0 && stale(ps.seen) {
			delete(s.prefixes, p)
		}
	}
	for k, p := range s.peers {
		if stale(p.seen) {
			delete(s.peers, k)
		}
	}
	for k, c := range s.collectors {
		if stale(c.seen) {
			delete(s.collectors, k)
		}
	}
	deleteStale(s.ases, stale)
	deleteStale(s.links, stale)
}

func deleteStale[K comparable](m map[K]time.Time, stale func(time.Time) bool) {
	for k, t := range m {
		if stale(t) {
			delete(m, k)
		}
	}
}

// trimPrefixes drops the least recently updated prefixes beyond the cap, never a watched one.
func (s *state) trimPrefixes() {
	for _, p := range overflow(s.prefixes, s.opts.MaxPrefixes, func(ps *prefixState) (time.Time, bool) { return ps.seen, ps.watched }) {
		delete(s.prefixes, p)
	}
}

// trim drops the least recently seen peers, collectors, ASes and links beyond each cap.
func (s *state) trim() {
	for _, k := range overflow(s.peers, maxPeers, func(p *peerState) (time.Time, bool) { return p.seen, false }) {
		delete(s.peers, k)
		s.dropRoutes(k)
	}
	for _, k := range overflow(s.collectors, maxCollectors, func(c *collectorState) (time.Time, bool) { return c.seen, false }) {
		delete(s.collectors, k)
	}
	for _, a := range overflow(s.ases, s.opts.MaxASes, plain) {
		delete(s.ases, a)
	}
	for _, l := range overflow(s.links, s.opts.MaxLinks, plain) {
		delete(s.links, l)
	}
}

func plain(t time.Time) (time.Time, bool) { return t, false }

// overflow lists the keys to drop to bring m down to limit, least recently seen first,
// never a pinned one.
func overflow[K comparable, V any](m map[K]V, limit int, seen func(V) (time.Time, bool)) []K {
	if len(m) <= limit {
		return nil
	}
	type aged struct {
		k K
		t time.Time
	}
	var out []aged
	for k, v := range m {
		if t, pinned := seen(v); !pinned {
			out = append(out, aged{k, t})
		}
	}
	slices.SortFunc(out, func(a, b aged) int { return a.t.Compare(b.t) })
	keys := make([]K, 0, len(m)-limit)
	for _, a := range out[:min(len(out), len(m)-limit)] {
		keys = append(keys, a.k)
	}
	return keys
}
