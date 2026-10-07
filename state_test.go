package bgp

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"wayseer.dev/sdk"
	"wayseer.dev/sdk/sdktest"
)

// fixtureOptions watch the fixture's prefixes, with an origin and expected origins.
const fixtureOptions = `prefixes: [203.0.113.0/24, 2001:db8::/32]
origins: [65551]
expect: {203.0.113.0/24: [64500], 2001:db8::/32: [65550]}`

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// newTestState is the working set for options, as Configure makes it.
func newTestState(t *testing.T, yaml string) *state {
	t.Helper()
	o, w, err := parsed(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	return newState("bgp", &o, w)
}

// feed applies RIS Live lines at a time.
func feed(t *testing.T, s *state, at time.Time, lines ...string) {
	t.Helper()
	for _, l := range lines {
		r, err := parse([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		if r.kind == kindMessage {
			s.apply(&r.msg, at)
		}
	}
}

// update is a RIS Live UPDATE from a peer at rrc00.
func update(id, peer string, path []uint32, announced []string, withdrawn ...string) string {
	return fmt.Sprintf(`{"type":"ris_message","data":{"id":%q,"host":"rrc00.ripe.net","type":"UPDATE","peer":%q,"peer_asn":"%d","path":%s,"announcements":[{"prefixes":%s}],"withdrawals":%s}}`,
		id, peer, path[0], jsonList(path), jsonList(announced), jsonList(withdrawn))
}

func jsonList[T any](vs []T) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprintf("%v", v)
		if s, ok := any(v).(string); ok {
			parts[i] = fmt.Sprintf("%q", s)
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// render lists the world's entities, then its edges, one per line.
func render(ents map[sdk.EntityRef]sdk.Entity, edges map[sdk.EdgeKey]sdk.Edge) string {
	var b strings.Builder
	for _, ref := range slices.Sorted(maps.Keys(ents)) {
		e := ents[ref]
		attrs := make([]string, 0, len(e.Attrs))
		for k, v := range e.Attrs {
			attrs = append(attrs, k+"="+v.String())
		}
		slices.Sort(attrs)
		fmt.Fprintf(&b, "%s %q %s %q %s\n", e.Ref, e.Name, e.Status.Level, e.Status.Reason, strings.Join(attrs, " "))
	}
	keys := slices.Collect(maps.Keys(edges))
	slices.SortFunc(keys, sdk.CompareEdgeKeys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %s %s\n", k.From, k.Rel, k.To)
	}
	return b.String()
}

func renderEvents(evs []sdk.Event) string {
	var b strings.Builder
	for _, e := range evs {
		fmt.Fprintf(&b, "%s %s %s: %s\n", e.Severity, e.Kind, e.Entity, e.Message)
	}
	return b.String()
}

func TestTheFixtureBuildsAWorld(t *testing.T) {
	s := newTestState(t, fixtureOptions)
	for _, line := range fixture(t) {
		feed(t, s, t0, string(line))
	}
	evs := s.flush(t0.Add(5 * time.Second))
	sdktest.Golden(t, "testdata/world.txt", render(s.world()))
	sdktest.Golden(t, "testdata/events.txt", renderEvents(evs))
}

func TestAnUnexpectedOriginIsAPossibleHijack(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]\nexpect: {203.0.113.0/24: [64500]}")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64511}, []string{"203.0.113.0/24"}))
	evs := s.flush(t0)
	if len(evs) != 1 || evs[0].Severity != sdk.SevCritical || !strings.Contains(evs[0].Message, "possible hijack") {
		t.Fatalf("events %s", renderEvents(evs))
	}
	if e := entity(t, s, kindPrefix, "203.0.113.0/24"); e.Status.Level != sdk.StatusCrit {
		t.Errorf("status %+v", e.Status)
	}
}

func TestANewOriginWithoutExpectationIsAWarning(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0,
		update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.0/24"}),
		update("2", "192.0.2.2", []uint32{64497, 64501}, []string{"203.0.113.0/24"}))
	evs := s.flush(t0)
	if len(evs) != 1 || evs[0].Severity != sdk.SevWarn || evs[0].Message != "203.0.113.0/24 announced by AS64501, was AS64500" {
		t.Fatalf("events %s", renderEvents(evs))
	}
	if e := entity(t, s, kindPrefix, "203.0.113.0/24"); e.Status.Level != sdk.StatusWarn {
		t.Errorf("status %+v", e.Status)
	}
}

func TestANewMoreSpecificIsAnEvent(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.128/25"}))
	evs := s.flush(t0)
	if len(evs) != 1 || evs[0].Kind != "more-specific" || evs[0].Entity.Native() != "203.0.113.128/25" {
		t.Fatalf("events %s", renderEvents(evs))
	}
	_, edges := s.world()
	parent := sdk.EdgeKey{From: s.ref(kindPrefix, "203.0.113.0/24"), To: s.ref(kindPrefix, "203.0.113.128/25"), Rel: sdk.RelParentOf}
	if _, ok := edges[parent]; !ok {
		t.Errorf("no %v", parent)
	}
}

func TestWithdrawalByEveryPeerTakesAPrefixDown(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.0/24"}))
	s.flush(t0)
	feed(t, s, t0, update("2", "192.0.2.1", []uint32{64496}, nil, "203.0.113.0/24"))
	evs := s.flush(t0.Add(time.Second))
	if len(evs) != 1 || evs[0].Severity != sdk.SevError || evs[0].Kind != "withdrawal" {
		t.Fatalf("events %s", renderEvents(evs))
	}
	if e := entity(t, s, kindPrefix, "203.0.113.0/24"); e.Status.Level != sdk.StatusDown {
		t.Errorf("status %+v", e.Status)
	}
}

func TestAPathChangeIsCountedAndAnEvent(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64497, 64500}, []string{"203.0.113.0/24"}))
	s.flush(t0)
	feed(t, s, t0, update("2", "192.0.2.1", []uint32{64496, 64498, 64500}, []string{"203.0.113.0/24"}))
	evs := s.flush(t0.Add(5 * time.Second))
	if len(evs) != 1 || evs[0].Kind != "path-change" || evs[0].Severity != sdk.SevInfo {
		t.Fatalf("events %s", renderEvents(evs))
	}
	got := s.series[sdk.SeriesRef{Entity: s.ref(kindPrefix, "203.0.113.0/24"), Metric: metricPathChanges}]
	if pts := got.In(sdk.TimeWindow{From: t0, To: t0.Add(time.Minute)}); len(pts) != 2 || pts[1].V != 0.2 {
		t.Errorf("path changes %v", pts)
	}
}

func TestAPeerGoingDownDropsItsRoutes(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.0/24"}))
	s.flush(t0)
	feed(t, s, t0, `{"type":"ris_message","data":{"id":"2","host":"rrc00.ripe.net","type":"RIS_PEER_STATE","peer":"192.0.2.1","peer_asn":"64496","state":"down"}}`)
	evs := s.flush(t0.Add(time.Second))
	if renderEvents(evs) != "warn peer-state bgp/bgp%2Fpeer/192.0.2.1@rrc00: peer 192.0.2.1@rrc00 (AS64496) is down; dropped its 1 route\n"+
		"error withdrawal bgp/bgp%2Fprefix/203.0.113.0%2F24: 203.0.113.0/24 is no longer carried by any peer seen\n" {
		t.Errorf("events\n%s", renderEvents(evs))
	}
	if e := entity(t, s, kindPeer, "192.0.2.1@rrc00"); e.Status.Level != sdk.StatusDown {
		t.Errorf("peer %+v", e.Status)
	}
}

func TestADuplicateMessageIsReadOnce(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	u := update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.0/24"})
	feed(t, s, t0, u, u)
	if n := s.prefixes[netip.MustParsePrefix("203.0.113.0/24")].ann; n != 1 {
		t.Errorf("%d announcements", n)
	}
}

func TestUnwatchedPrefixesAreIgnored(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]")
	feed(t, s, t0, update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"198.51.100.0/24"}))
	ents, _ := s.world()
	if len(ents) != 1 {
		t.Errorf("world %s", render(s.world()))
	}
}

func TestTheWorkingSetIsBounded(t *testing.T) {
	s := newTestState(t, "origins: [64500]\nmax_ases: 3\nmax_links: 2\nmax_prefixes: 2")
	for i := range 10 {
		at := t0.Add(time.Duration(i) * time.Second)
		path := []uint32{64496, uint32(65000 + i), 64500}
		feed(t, s, at, update(fmt.Sprint(i), "192.0.2.1", path, []string{fmt.Sprintf("198.51.%d.0/24", i)}))
	}
	s.flush(t0.Add(time.Minute))
	if len(s.ases) > 3 || len(s.links) > 2 || len(s.prefixes) > 2 {
		t.Errorf("%d ASes, %d links, %d prefixes", len(s.ases), len(s.links), len(s.prefixes))
	}
	if _, ok := s.prefixes[netip.MustParsePrefix("198.51.9.0/24")]; !ok {
		t.Error("the newest prefix was dropped")
	}
}

func TestWhatIsNoLongerSeenAgesOut(t *testing.T) {
	s := newTestState(t, "prefixes: [203.0.113.0/24]\norigins: [64500]\nstale_after: 1m")
	feed(t, s, t0,
		update("1", "192.0.2.1", []uint32{64496, 64500}, []string{"203.0.113.0/24"}),
		update("2", "192.0.2.2", []uint32{64497, 64500}, []string{"198.51.100.0/24"}),
		update("3", "192.0.2.2", []uint32{64497}, nil, "198.51.100.0/24"))
	s.flush(t0.Add(2 * time.Minute))
	got := render(s.world())
	if strings.Contains(got, "198.51.100.0") || strings.Contains(got, "192.0.2.2") || strings.Contains(got, "AS64497") {
		t.Errorf("stale entities kept:\n%s", got)
	}
	if !strings.Contains(got, "192.0.2.1@rrc00") || !strings.Contains(got, "AS64500") {
		t.Errorf("a carried route's peer or AS aged out:\n%s", got)
	}
}

// entity is one entity of the state's world.
func entity(t *testing.T, s *state, kind sdk.Kind, native string) sdk.Entity {
	t.Helper()
	ents, _ := s.world()
	e, ok := ents[s.ref(kind, native)]
	if !ok {
		t.Fatalf("no %s %s in\n%s", kind, native, render(s.world()))
	}
	return e
}
