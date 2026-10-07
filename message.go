package bgp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Limits on one message; RIS Live stays far below them.
const (
	maxPath     = 256
	maxPrefixes = 8192
)

// What a message from RIS Live is to the module.
const (
	kindMessage = "message" // a ris_message of a type the module reads
	kindError   = "error"   // a ris_error
	kindIgnored = "ignored" // anything else: pong, ris_subscribe_ok, other BGP types
)

// received is one message from RIS Live, read.
type received struct {
	kind string
	msg  msg    // for kindMessage
	err  string // for kindError
}

// msg is one ris_message of a type the module reads, checked.
type msg struct {
	id        string
	at        time.Time
	kind      string // typeUpdate or typePeerState
	collector string // the collector's short name, such as rrc00
	peer      netip.Addr
	peerASN   uint32
	path      []uint32 // peer first, prepends collapsed; 0 stands for an AS set of several ASes
	announced []netip.Prefix
	withdrawn []netip.Prefix
	state     string // for typePeerState: connected, up or down
}

// origin is the last AS of the path, or 0 when it is unknown.
func (m *msg) origin() uint32 {
	if len(m.path) == 0 {
		return 0
	}
	return m.path[len(m.path)-1]
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// wire is a ris_message's data as RIS Live writes it.
type wire struct {
	Timestamp     float64           `json:"timestamp"`
	Peer          string            `json:"peer"`
	PeerASN       string            `json:"peer_asn"`
	ID            string            `json:"id"`
	Host          string            `json:"host"`
	Type          string            `json:"type"`
	Path          []json.RawMessage `json:"path"`
	Announcements []struct {
		Prefixes []string `json:"prefixes"`
	} `json:"announcements"`
	Withdrawals []string `json:"withdrawals"`
	State       string   `json:"state"`
}

// parse reads one message from RIS Live; errors say what was wrong with it.
func parse(line []byte) (received, error) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return received{}, err
	}
	switch env.Type {
	case "ris_error":
		var e struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(env.Data, &e); err != nil {
			return received{}, fmt.Errorf("ris_error: %w", err)
		}
		return received{kind: kindError, err: e.Message}, nil
	case "ris_message":
		return parseMessage(env.Data)
	}
	return received{kind: kindIgnored}, nil
}

func parseMessage(data []byte) (received, error) {
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return received{}, fmt.Errorf("ris_message: %w", err)
	}
	if w.Type != typeUpdate && w.Type != typePeerState {
		return received{kind: kindIgnored}, nil
	}
	m, err := w.header()
	if err != nil {
		return received{}, fmt.Errorf("ris_message %q: %w", w.ID, err)
	}
	if m.path, err = readPath(w.Path); err == nil {
		m.announced, m.withdrawn, err = w.prefixes()
	}
	if err != nil {
		return received{}, fmt.Errorf("ris_message %q: %w", w.ID, err)
	}
	return received{kind: kindMessage, msg: m}, nil
}

// header reads who sent the message, when and through which collector.
func (w *wire) header() (msg, error) {
	collector, _, _ := strings.Cut(w.Host, ".")
	peer, err := netip.ParseAddr(w.Peer)
	if err != nil {
		return msg{}, fmt.Errorf("peer %q is not an address", w.Peer)
	}
	asn, err := strconv.ParseUint(w.PeerASN, 10, 32)
	switch {
	case err != nil || asn == 0:
		return msg{}, fmt.Errorf("peer_asn %q is not an ASN", w.PeerASN)
	case collector == "" || len(collector) > 64:
		return msg{}, fmt.Errorf("host %q is not a collector", w.Host)
	}
	return msg{
		id: w.ID, at: stamp(w.Timestamp), kind: w.Type, collector: collector,
		peer: peer.Unmap(), peerASN: uint32(asn), state: w.State,
	}, nil
}

// stamp turns RIS Live's seconds into a time, or now when they make no sense.
func stamp(secs float64) time.Time {
	if secs <= 0 || secs > math.MaxInt32*4 {
		return time.Now()
	}
	return time.UnixMilli(int64(math.Round(secs * 1000)))
}

// readPath reads an AS path, collapsing prepends; an AS set of one is that AS, a wider one 0.
func readPath(raw []json.RawMessage) ([]uint32, error) {
	if len(raw) > maxPath {
		return nil, fmt.Errorf("path of %d hops is longer than %d", len(raw), maxPath)
	}
	out := make([]uint32, 0, len(raw))
	for _, hop := range raw {
		asn, err := readHop(hop)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1] != asn || asn == 0 {
			out = append(out, asn)
		}
	}
	return out, nil
}

func readHop(hop json.RawMessage) (uint32, error) {
	var asn uint32
	if err := json.Unmarshal(hop, &asn); err == nil {
		if asn == 0 {
			return 0, errors.New("path holds AS0")
		}
		return asn, nil
	}
	var set []uint32
	if err := json.Unmarshal(hop, &set); err != nil || len(set) == 0 || len(set) > maxPath {
		return 0, fmt.Errorf("path hop %s is not an ASN or an AS set", hop)
	}
	if len(set) > 1 || set[0] == 0 {
		return 0, nil
	}
	return set[0], nil
}

func (w *wire) prefixes() (announced, withdrawn []netip.Prefix, err error) {
	n := len(w.Withdrawals)
	for _, a := range w.Announcements {
		n += len(a.Prefixes)
	}
	if n > maxPrefixes {
		return nil, nil, fmt.Errorf("%d prefixes, more than %d", n, maxPrefixes)
	}
	for _, a := range w.Announcements {
		if announced, err = appendPrefixes(announced, a.Prefixes); err != nil {
			return nil, nil, err
		}
	}
	withdrawn, err = appendPrefixes(nil, w.Withdrawals)
	return announced, withdrawn, err
}

func appendPrefixes(out []netip.Prefix, in []string) ([]netip.Prefix, error) {
	for _, s := range in {
		p, err := netip.ParsePrefix(s)
		if err != nil || p != p.Masked() {
			return nil, fmt.Errorf("prefix %q is not a network", s)
		}
		out = append(out, p)
	}
	return out, nil
}
