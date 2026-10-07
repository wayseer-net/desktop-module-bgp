package bgp

import (
	"net/netip"
	"slices"
	"strconv"
)

// RIS Live message types the module reads.
const (
	typeUpdate    = "UPDATE"
	typePeerState = "RIS_PEER_STATE"
)

// subscription is one ris_subscribe filter; RIS Live sends what matches any of a socket's.
type subscription struct {
	Host         string   `json:"host,omitempty"`
	Peer         string   `json:"peer,omitempty"`
	Type         string   `json:"type"`
	Path         string   `json:"path,omitempty"`
	Prefix       []string `json:"prefix,omitempty"`
	MoreSpecific *bool    `json:"moreSpecific,omitempty"`
	LessSpecific *bool    `json:"lessSpecific,omitempty"`
}

// subscriptions are the filters for the options: per host and peer, the watched prefixes,
// each origin as the last AS of the path, and peer state.
func subscriptions(o *options) []subscription {
	var out []subscription
	for _, host := range orAll(o.Hosts) {
		for _, peer := range orAll(o.Peers) {
			out = append(out, scoped(o, subscription{Host: host, Peer: peer})...)
		}
	}
	return out
}

func scoped(o *options, scope subscription) []subscription {
	var out []subscription
	if slices.Contains(o.Types, typeUpdate) {
		if len(o.Prefixes) > 0 {
			s := scope
			s.Type, s.Prefix = typeUpdate, canonical(o.Prefixes)
			s.MoreSpecific, s.LessSpecific = &o.MoreSpecific, &o.LessSpecific
			out = append(out, s)
		}
		for _, asn := range o.Origins {
			s := scope
			s.Type, s.Path = typeUpdate, strconv.FormatUint(uint64(asn), 10)+"$"
			out = append(out, s)
		}
	}
	if slices.Contains(o.Types, typePeerState) {
		s := scope
		s.Type = typePeerState
		out = append(out, s)
	}
	return out
}

// canonical writes valid prefixes as netip does, so RIS Live sees one spelling.
func canonical(prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, s := range prefixes {
		if p, err := netip.ParsePrefix(s); err == nil {
			s = p.String()
		}
		out = append(out, s)
	}
	return out
}

// orAll is the names given, or one empty name meaning all.
func orAll(names []string) []string {
	if len(names) == 0 {
		return []string{""}
	}
	return names
}
