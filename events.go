package bgp

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"wayseer.dev/sdk"
)

// Event bounds: peers named in one event, and events held between sends.
const (
	maxNamed   = 5
	maxPending = 500
)

// emit queues an event for the next send, numbering it so its ID is unique.
func (s *state) emit(e sdk.Event) {
	if len(s.pending) >= maxPending {
		s.dropped++
		return
	}
	s.seq++
	e.ID = e.Kind + "/" + strconv.FormatInt(e.At.UnixNano(), 10) + "/" + strconv.FormatUint(s.seq, 10)
	e.Source = s.src
	s.pending = append(s.pending, e)
}

func hijackEvent(ref sdk.EntityRef, p netip.Prefix, origin uint32, expected []uint32, m *msg) sdk.Event {
	return sdk.Event{
		Entity: ref, At: m.at, Severity: sdk.SevCritical, Kind: "origin",
		Message: fmt.Sprintf("possible hijack: %s announced by AS%d, expected %s (seen by %s)", p, origin, asList(expected), keyOf(m)),
		Fields:  fields(origin, m),
	}
}

func moreSpecificEvent(ref sdk.EntityRef, p, parent netip.Prefix, m *msg) sdk.Event {
	return sdk.Event{
		Entity: ref, At: m.at, Severity: sdk.SevWarn, Kind: "more-specific",
		Message: fmt.Sprintf("new more-specific %s of %s, announced by AS%d", p, parent, m.origin()),
		Fields:  fields(m.origin(), m),
	}
}

func originEvent(ref sdk.EntityRef, p netip.Prefix, origin uint32, was []uint32, m *msg) sdk.Event {
	return sdk.Event{
		Entity: ref, At: m.at, Severity: sdk.SevWarn, Kind: "origin",
		Message: fmt.Sprintf("%s announced by AS%d, was %s", p, origin, asList(was)),
		Fields:  fields(origin, m),
	}
}

func peerEvent(ref sdk.EntityRef, key peerKey, p *peerState, dropped int, at time.Time) sdk.Event {
	e := sdk.Event{
		Entity: ref, At: at, Severity: sdk.SevInfo, Kind: "peer-state",
		Message: fmt.Sprintf("peer %s (AS%d) is up", key, p.asn),
	}
	if p.down {
		e.Severity = sdk.SevWarn
		e.Message = fmt.Sprintf("peer %s (AS%d) is down; dropped its %d route%s", key, p.asn, dropped, plural(dropped))
	}
	return e
}

func fields(origin uint32, m *msg) map[string]sdk.Value {
	return map[string]sdk.Value{
		"origin": sdk.Number(float64(origin)), "peer": sdk.String(string(keyOf(m))), "peer_asn": sdk.Number(float64(m.peerASN)),
	}
}

// summarise raises, for each watched prefix, one event for its withdrawals and one for its
// path changes since the last send.
func (s *state) summarise(now time.Time) {
	for _, p := range slices.SortedFunc(maps.Keys(s.prefixes), comparePrefixes) {
		ps := s.prefixes[p]
		ref := s.ref(kindPrefix, p.String())
		if ps.watched && (ps.wd > 0 || ps.visible && len(ps.routes) == 0) {
			s.emit(withdrawalEvent(ref, p, ps, now))
		}
		if ps.watched && ps.changes > 0 {
			s.emit(sdk.Event{
				Entity: ref, At: now, Severity: sdk.SevInfo, Kind: "path-change",
				Message: fmt.Sprintf("path to %s changed %d time%s", p, ps.changes, plural(ps.changes)),
			})
		}
		ps.visible, ps.withdrawnBy, ps.othersWithdrew = len(ps.routes) > 0, nil, false
	}
}

func withdrawalEvent(ref sdk.EntityRef, p netip.Prefix, ps *prefixState, now time.Time) sdk.Event {
	e := sdk.Event{Entity: ref, At: now, Severity: sdk.SevInfo, Kind: "withdrawal"}
	by := ""
	if len(ps.withdrawnBy) > 0 {
		by = fmt.Sprintf("withdrawn by %s; ", peerList(ps.withdrawnBy, ps.othersWithdrew))
	}
	switch n := len(ps.routes); {
	case n == 0 && by == "":
		e.Severity, e.Message = sdk.SevError, fmt.Sprintf("%s is no longer carried by any peer seen", p)
	case n == 0:
		e.Severity, e.Message = sdk.SevError, fmt.Sprintf("%s %sno peer seen carries it now", p, by)
	default:
		e.Message = fmt.Sprintf("%s %s%s", p, by, stillCarried(n))
	}
	return e
}

// peerList names the first peers, and says when there were others.
func peerList(keys []peerKey, others bool) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = string(k)
	}
	out := strings.Join(names, ", ")
	if others {
		out += " and others"
	}
	return out
}

func asList(asns []uint32) string {
	out := make([]string, len(asns))
	for i, a := range asns {
		out[i] = "AS" + strconv.FormatUint(uint64(a), 10)
	}
	return strings.Join(out, " or ")
}

func stillCarried(n int) string {
	if n == 1 {
		return "1 peer still carries it"
	}
	return fmt.Sprintf("%d peers still carry it", n)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func comparePrefixes(a, b netip.Prefix) int {
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c
	}
	return a.Bits() - b.Bits()
}
