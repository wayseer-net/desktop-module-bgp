package bgp

import (
	"net/netip"
	"slices"
	"time"

	"wayseer.dev/sdk"
)

// Kinds the module sends, all in its namespace.
const (
	kindAS        sdk.Kind = "bgp/as"
	kindPrefix    sdk.Kind = "bgp/prefix"
	kindPeer      sdk.Kind = "bgp/peer"
	kindCollector sdk.Kind = "bgp/collector"
)

// Bounds the options don't set.
const (
	maxPeers      = 1000
	maxCollectors = 64
	recentIDs     = 4096 // message IDs remembered, to read a message sent twice once
)

// peerKey names a peer at one collector: its address, @, and the collector.
type peerKey string

func keyOf(m *msg) peerKey { return peerKey(m.peer.String() + "@" + m.collector) }

// counts are what happened since the last send.
type counts struct{ ann, wd, changes int }

type prefixState struct {
	counts
	watched        bool
	parent         netip.Prefix         // the watched prefix it is, is inside, or covers; none for an origin's
	routes         map[peerKey][]uint32 // each carrying peer's AS path
	seen           time.Time            // last update about it
	routed         bool                 // carried by some peer since the module started
	visible        bool                 // carried by some peer at the last send
	withdrawnBy    []peerKey            // since the last send, the first few
	othersWithdrew bool                 // and more than those withdrew it
}

type peerState struct {
	counts
	addr      netip.Addr
	collector string
	asn       uint32
	down      bool
	seen      time.Time
}

type collectorState struct {
	counts
	seen time.Time
}

// link is an adjacency in an AS path: from is nearer the peer, to nearer the origin.
type link struct{ from, to uint32 }

// state is the module's working set: what RIS Live said, bounded and ageing.
type state struct {
	src        sdk.ModuleID
	opts       options
	w          watch
	prefixes   map[netip.Prefix]*prefixState
	peers      map[peerKey]*peerState
	collectors map[string]*collectorState
	ases       map[uint32]time.Time
	links      map[link]time.Time
	series     map[sdk.SeriesRef]*sdk.Ring
	flushed    time.Time // the last send
	pending    []sdk.Event
	dropped    int // events left out since the last send
	seq        uint64
	recent     recent
}

func newState(src sdk.ModuleID, o *options, w watch) *state {
	s := &state{
		src: src, opts: *o, w: w,
		prefixes: map[netip.Prefix]*prefixState{}, peers: map[peerKey]*peerState{},
		collectors: map[string]*collectorState{}, ases: map[uint32]time.Time{}, links: map[link]time.Time{},
		series: map[sdk.SeriesRef]*sdk.Ring{},
	}
	for _, p := range w.prefixes {
		s.prefixes[p] = &prefixState{watched: true, parent: p, routes: map[peerKey][]uint32{}}
	}
	return s
}

// apply folds one message into the working set.
func (s *state) apply(m *msg, now time.Time) {
	if m.id != "" && !s.recent.fresh(m.collector+"/"+m.id) {
		return
	}
	switch m.kind {
	case typeUpdate:
		s.update(m, now)
	case typePeerState:
		s.peerChange(m, now)
	}
}

func (s *state) update(m *msg, now time.Time) {
	key, n := keyOf(m), counts{}
	for _, p := range m.announced {
		if ps := s.relevant(p, m.origin(), now); ps != nil {
			s.announce(p, ps, key, m)
			ps.seen = now
			n.ann++
		}
	}
	for _, p := range m.withdrawn {
		if ps := s.prefixes[p]; ps != nil {
			s.withdraw(ps, key)
			ps.seen = now
			n.wd++
		}
	}
	if n.ann+n.wd > 0 {
		s.saw(m, key, n, now)
	}
}

// relevant is the state of a prefix the options watch, made if new; nil for any other.
func (s *state) relevant(p netip.Prefix, origin uint32, now time.Time) *prefixState {
	if ps := s.prefixes[p]; ps != nil {
		return ps
	}
	parent, ok := s.w.relation(p, origin)
	if !ok {
		return nil
	}
	ps := &prefixState{parent: parent, routes: map[peerKey][]uint32{}, seen: now}
	s.prefixes[p] = ps
	return ps
}

// relation is the watched prefix p is inside or covers, and whether p is watched at all.
func (w *watch) relation(p netip.Prefix, origin uint32) (netip.Prefix, bool) {
	for _, q := range w.prefixes {
		if w.more && p.Bits() > q.Bits() && q.Contains(p.Addr()) || w.less && p.Bits() < q.Bits() && p.Contains(q.Addr()) {
			return q, true
		}
	}
	return netip.Prefix{}, origin != 0 && slices.Contains(w.origins, origin)
}

func (s *state) announce(p netip.Prefix, ps *prefixState, key peerKey, m *msg) {
	s.checkOrigin(p, ps, m)
	if old, had := ps.routes[key]; had && !slices.Equal(old, m.path) {
		ps.changes++
	}
	ps.routes[key] = m.path
	ps.ann++
	ps.routed = true
}

// checkOrigin raises an event for an origin new to the prefix: a possible hijack when it isn't
// expected, a new more-specific, or a change of origin.
func (s *state) checkOrigin(p netip.Prefix, ps *prefixState, m *msg) {
	o := m.origin()
	if o == 0 || slices.Contains(ps.origins(), o) {
		return
	}
	expected := s.expected(p, ps)
	switch {
	case len(expected) > 0 && !slices.Contains(expected, o):
		s.emit(hijackEvent(s.ref(kindPrefix, p.String()), p, o, expected, m))
	case !ps.routed && ps.parent.IsValid() && p.Bits() > ps.parent.Bits():
		s.emit(moreSpecificEvent(s.ref(kindPrefix, p.String()), p, ps.parent, m))
	case len(ps.routes) > 0 && len(expected) == 0:
		s.emit(originEvent(s.ref(kindPrefix, p.String()), p, o, ps.origins(), m))
	}
}

// expected are the legitimate origins of p: those of the watched prefix it is or is inside.
func (s *state) expected(p netip.Prefix, ps *prefixState) []uint32 {
	if !ps.parent.IsValid() || p.Bits() < ps.parent.Bits() {
		return nil
	}
	return s.w.expect[ps.parent]
}

// origins are the distinct known origins of the prefix's routes, in order.
func (ps *prefixState) origins() []uint32 {
	var out []uint32
	for _, path := range ps.routes {
		if len(path) == 0 {
			continue
		}
		if o := path[len(path)-1]; o != 0 && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return out
}

func (s *state) withdraw(ps *prefixState, key peerKey) {
	delete(ps.routes, key)
	ps.wd++
	switch {
	case !ps.watched || slices.Contains(ps.withdrawnBy, key):
	case len(ps.withdrawnBy) < maxNamed:
		ps.withdrawnBy = append(ps.withdrawnBy, key)
	default:
		ps.othersWithdrew = true
	}
}

// saw records the peer, collector and ASes of an update that touched a watched prefix.
func (s *state) saw(m *msg, key peerKey, n counts, now time.Time) {
	p := s.peers[key]
	if p == nil {
		p = &peerState{addr: m.peer, collector: m.collector}
		s.peers[key] = p
	}
	p.asn, p.down, p.seen = m.peerASN, false, now
	p.ann, p.wd = p.ann+n.ann, p.wd+n.wd
	c := s.collectors[m.collector]
	if c == nil {
		c = &collectorState{}
		s.collectors[m.collector] = c
	}
	c.seen, c.ann, c.wd = now, c.ann+n.ann, c.wd+n.wd
	s.ases[m.peerASN] = now
	s.touchPath(m.path, now)
}

// touchPath marks a path's ASes and adjacencies seen; an AS set breaks the chain.
func (s *state) touchPath(path []uint32, now time.Time) {
	for i, a := range path {
		if a == 0 {
			continue
		}
		s.ases[a] = now
		if i > 0 && path[i-1] != 0 {
			s.links[link{path[i-1], a}] = now
		}
	}
}

// peerChange follows a known peer's session going down or coming back.
func (s *state) peerChange(m *msg, now time.Time) {
	key := keyOf(m)
	p := s.peers[key]
	if p == nil {
		return
	}
	p.seen = now
	if down := m.state == "down"; down != p.down {
		p.down = down
		n := 0
		if down {
			n = s.dropRoutes(key)
		}
		s.emit(peerEvent(s.ref(kindPeer, string(key)), key, p, n, m.at))
	}
}

// dropRoutes forgets every route the peer carried and says how many there were.
func (s *state) dropRoutes(key peerKey) int {
	n := 0
	for _, ps := range s.prefixes {
		if _, ok := ps.routes[key]; ok {
			delete(ps.routes, key)
			n++
		}
	}
	return n
}

// ref is the entity ref of a native ID; the instance name was checked in Configure.
func (s *state) ref(kind sdk.Kind, native string) sdk.EntityRef {
	r, _ := sdk.NewEntityRef(string(s.src), kind, native)
	return r
}

// recent remembers the last message IDs, so one that matched two subscriptions is read once.
type recent struct {
	seen  map[string]struct{}
	order []string
	next  int
}

func (r *recent) fresh(id string) bool {
	if r.seen == nil {
		r.seen, r.order = map[string]struct{}{}, make([]string, recentIDs)
	}
	if _, ok := r.seen[id]; ok {
		return false
	}
	delete(r.seen, r.order[r.next])
	r.order[r.next] = id
	r.next = (r.next + 1) % len(r.order)
	r.seen[id] = struct{}{}
	return true
}
