package bgp

import (
	"net/netip"
	"slices"
	"strconv"

	"wayseer.dev/sdk"
)

// builder gathers the working set as entities and edges, keyed as sdk.Tracker wants them.
type builder struct {
	ents  map[sdk.EntityRef]sdk.Entity
	edges map[sdk.EdgeKey]sdk.Edge
}

// world builds the entities, then the edges between those that exist.
func (s *state) world() (map[sdk.EntityRef]sdk.Entity, map[sdk.EdgeKey]sdk.Edge) {
	w := builder{ents: map[sdk.EntityRef]sdk.Entity{}, edges: map[sdk.EdgeKey]sdk.Edge{}}
	for name := range s.collectors {
		w.add(s.entity(kindCollector, name, sdk.Status{Level: sdk.StatusOK}, nil))
	}
	for key, p := range s.peers {
		w.add(s.entity(kindPeer, string(key), peerStatus(p), peerAttrs(p)))
	}
	for asn := range s.ases {
		w.add(s.entity(kindAS, asName(asn), sdk.Status{Level: sdk.StatusOK}, map[string]sdk.Value{"asn": sdk.Number(float64(asn))}))
	}
	for p, ps := range s.prefixes {
		w.add(s.entity(kindPrefix, p.String(), s.prefixStatus(p, ps), prefixAttrs(p, ps)))
	}
	s.peerEdges(&w)
	s.pathEdges(&w)
	return w.ents, w.edges
}

func (s *state) entity(kind sdk.Kind, native string, st sdk.Status, attrs map[string]sdk.Value) sdk.Entity {
	return sdk.Entity{Ref: s.ref(kind, native), Kind: kind, Name: native, Status: st, Attrs: attrs, Source: s.src}
}

func (w *builder) add(e sdk.Entity) { w.ents[e.Ref] = e }

// link adds an edge when both its ends are in the world.
func (w *builder) link(from, to sdk.EntityRef, rel sdk.Relation, src sdk.ModuleID) {
	_, a := w.ents[from]
	_, b := w.ents[to]
	if a && b {
		w.edges[sdk.EdgeKey{From: from, To: to, Rel: rel}] = sdk.Edge{From: from, To: to, Rel: rel, Weight: 1, Source: src}
	}
}

// peerEdges: each peer is a member of its collector, and owned by its AS.
func (s *state) peerEdges(w *builder) {
	for key, p := range s.peers {
		ref := s.ref(kindPeer, string(key))
		w.link(ref, s.ref(kindCollector, p.collector), sdk.RelMemberOf, s.src)
		w.link(s.ref(kindAS, asName(p.asn)), ref, sdk.RelOwns, s.src)
	}
}

// pathEdges: ASes talk to the next AS toward the origin, an origin owns its prefix, and a
// watched prefix is the parent of its more-specifics (a less-specific, of it).
func (s *state) pathEdges(w *builder) {
	for l := range s.links {
		w.link(s.ref(kindAS, asName(l.from)), s.ref(kindAS, asName(l.to)), sdk.RelTalksTo, s.src)
	}
	for p, ps := range s.prefixes {
		ref := s.ref(kindPrefix, p.String())
		for _, o := range ps.origins() {
			w.link(s.ref(kindAS, asName(o)), ref, sdk.RelOwns, s.src)
		}
		switch parent := s.ref(kindPrefix, ps.parent.String()); {
		case ps.watched || !ps.parent.IsValid():
		case p.Bits() > ps.parent.Bits():
			w.link(parent, ref, sdk.RelParentOf, s.src)
		default:
			w.link(ref, parent, sdk.RelParentOf, s.src)
		}
	}
}

// prefixStatus is crit for an unexpected origin, down once every peer withdrew it, warn for
// more than one origin, and unknown until an announcement is seen.
func (s *state) prefixStatus(p netip.Prefix, ps *prefixState) sdk.Status {
	origins := ps.origins()
	if expected := s.expected(p, ps); len(expected) > 0 {
		for _, o := range origins {
			if !slices.Contains(expected, o) {
				return sdk.Status{Level: sdk.StatusCrit, Reason: "announced by AS" + strconv.FormatUint(uint64(o), 10) + ", not an expected origin"}
			}
		}
	}
	switch {
	case len(ps.routes) == 0 && ps.routed:
		return sdk.Status{Level: sdk.StatusDown, Reason: "withdrawn by every peer seen carrying it"}
	case len(ps.routes) == 0:
		return sdk.Status{Level: sdk.StatusUnknown, Reason: "no announcement seen since connecting"}
	case len(origins) > 1:
		return sdk.Status{Level: sdk.StatusWarn, Reason: "announced by more than one origin: " + asList(origins)}
	}
	return sdk.Status{Level: sdk.StatusOK}
}

func prefixAttrs(p netip.Prefix, ps *prefixState) map[string]sdk.Value {
	origins := make([]sdk.Value, 0, 1)
	for _, o := range ps.origins() {
		origins = append(origins, sdk.String(asName(o)))
	}
	attrs := map[string]sdk.Value{
		"watched": sdk.Bool(ps.watched), "peers": sdk.Number(float64(len(ps.routes))),
		"origins": sdk.List(origins...), "length": sdk.Number(float64(p.Bits())),
	}
	if ps.parent.IsValid() && !ps.watched {
		attrs["watched_prefix"] = sdk.String(ps.parent.String())
	}
	return attrs
}

func peerStatus(p *peerState) sdk.Status {
	if p.down {
		return sdk.Status{Level: sdk.StatusDown, Reason: "RIS reports the BGP session down"}
	}
	return sdk.Status{Level: sdk.StatusOK}
}

func peerAttrs(p *peerState) map[string]sdk.Value {
	return map[string]sdk.Value{
		"peer_ip": sdk.String(p.addr.String()), "asn": sdk.Number(float64(p.asn)), "collector": sdk.String(p.collector),
	}
}

func asName(asn uint32) string { return "AS" + strconv.FormatUint(uint64(asn), 10) }
