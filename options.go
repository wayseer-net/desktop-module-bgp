package bgp

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"time"
)

// Limits on what one instance may watch, so it never asks RIS Live for a firehose.
const (
	maxWatched       = 100 // prefixes
	maxOrigins       = 32
	maxScopes        = 32 // hosts, and peers, each
	maxSubscriptions = 64
	minV4Bits        = 8 // the widest prefixes watched with their more-specifics
	minV6Bits        = 16
)

type options struct {
	URL          string              `yaml:"url"`           // RIS Live: wss:// or ws://, or its stream at https:// or http://
	Client       string              `yaml:"client"`        // the client identifier sent to RIS Live
	Prefixes     []string            `yaml:"prefixes"`      // prefixes to watch, such as 193.0.0.0/21
	MoreSpecific bool                `yaml:"more_specific"` // also watch prefixes inside them
	LessSpecific bool                `yaml:"less_specific"` // also watch prefixes that contain them
	Origins      []uint32            `yaml:"origins"`       // ASNs whose every announcement is watched
	Expect       map[string][]uint32 `yaml:"expect"`        // a watched prefix's legitimate origin ASNs
	Hosts        []string            `yaml:"hosts"`         // collectors to read, such as rrc00; all if none
	Peers        []string            `yaml:"peers"`         // peer addresses to read; all if none
	Types        []string            `yaml:"types"`         // message types: UPDATE, RIS_PEER_STATE
	Interval     time.Duration       `yaml:"interval"`      // how often changes and series points are sent
	StaleAfter   time.Duration       `yaml:"stale_after"`   // how long something with no route is kept unseen
	MaxASes      int                 `yaml:"max_ases"`      // most ASes kept; the least recently seen go first
	MaxLinks     int                 `yaml:"max_links"`     // most AS adjacencies kept
	MaxPrefixes  int                 `yaml:"max_prefixes"`  // most prefixes kept, watched ones always
}

// watch is what the options select, parsed.
type watch struct {
	prefixes   []netip.Prefix
	expect     map[netip.Prefix][]uint32
	origins    []uint32
	more, less bool
}

func defaults() options {
	return options{
		URL: "wss://ris-live.ripe.net/v1/ws/", Client: "wayseer-bgp", MoreSpecific: true,
		Types: []string{typeUpdate, typePeerState}, Interval: 5 * time.Second, StaleAfter: time.Hour,
		MaxASes: 2000, MaxLinks: 5000, MaxPrefixes: 200,
	}
}

var (
	clientID  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	collector = regexp.MustCompile(`^rrc[0-9]{2}$`)
)

// validate checks every option and returns what they select.
func (o *options) validate() (watch, error) {
	w := watch{more: o.MoreSpecific, less: o.LessSpecific, origins: o.Origins}
	var err error
	w.prefixes, err = o.parsePrefixes()
	errs := []error{err, o.checkScalars(), checkURL(o.URL), o.checkOrigins(), o.checkScopes(), o.checkTypes()}
	w.expect, err = o.parseExpect(w.prefixes)
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return watch{}, err
	}
	if len(w.prefixes) == 0 && len(w.origins) == 0 || !slices.Contains(o.Types, typeUpdate) {
		return watch{}, errors.New("set prefixes or origins, and keep UPDATE in types: RIS Live is never read unfiltered")
	}
	if n := len(subscriptions(o)); n > maxSubscriptions {
		return watch{}, fmt.Errorf("the options make %d subscriptions, at most %d: name fewer hosts, peers or origins", n, maxSubscriptions)
	}
	return w, nil
}

func (o *options) checkScalars() error {
	switch {
	case !clientID.MatchString(o.Client):
		return fmt.Errorf("client %q must be 1 to 64 letters, digits, '.', '_' or '-'", o.Client)
	case o.Interval < time.Second || o.Interval > time.Minute:
		return fmt.Errorf("interval %v must be from 1s to 1m", o.Interval)
	case o.StaleAfter < o.Interval || o.StaleAfter > 24*time.Hour:
		return fmt.Errorf("stale_after %v must be from the interval to 24h", o.StaleAfter)
	case o.MaxASes < 1 || o.MaxASes > 100_000:
		return fmt.Errorf("max_ases %d must be from 1 to 100000", o.MaxASes)
	case o.MaxLinks < 1 || o.MaxLinks > 200_000:
		return fmt.Errorf("max_links %d must be from 1 to 200000", o.MaxLinks)
	case o.MaxPrefixes < 1 || o.MaxPrefixes > 10_000:
		return fmt.Errorf("max_prefixes %d must be from 1 to 10000", o.MaxPrefixes)
	}
	return nil
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Errorf("url: %w", err)
	case !slices.Contains([]string{"ws", "wss", "http", "https"}, u.Scheme) || u.Host == "":
		return fmt.Errorf("url %q must be wss://, ws://, https:// or http:// with a host", raw)
	case u.User != nil:
		return errors.New("url must not hold credentials")
	}
	return nil
}

func (o *options) parsePrefixes() ([]netip.Prefix, error) {
	if len(o.Prefixes) > maxWatched {
		return nil, fmt.Errorf("prefixes: %d named, at most %d", len(o.Prefixes), maxWatched)
	}
	out := make([]netip.Prefix, 0, len(o.Prefixes))
	for _, s := range o.Prefixes {
		p, err := watchedPrefix(s, o.MoreSpecific)
		if err != nil {
			return nil, err
		}
		if slices.Contains(out, p) {
			return nil, fmt.Errorf("prefix %s is named twice", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// watchedPrefix parses one watched prefix; with more-specifics it must not be too wide.
func watchedPrefix(s string, more bool) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	switch {
	case err != nil:
		return p, fmt.Errorf("prefix %q: %w", s, err)
	case p != p.Masked():
		return p, fmt.Errorf("prefix %q has host bits set; write %s", s, p.Masked())
	case more && p.Addr().Is4() && p.Bits() < minV4Bits, more && p.Addr().Is6() && p.Bits() < minV6Bits:
		return p, fmt.Errorf("prefix %s is too wide to watch with its more-specifics (at least /%d for IPv4, /%d for IPv6)", p, minV4Bits, minV6Bits)
	}
	return p, nil
}

func (o *options) parseExpect(watched []netip.Prefix) (map[netip.Prefix][]uint32, error) {
	out := make(map[netip.Prefix][]uint32, len(o.Expect))
	for s, asns := range o.Expect {
		p, err := netip.ParsePrefix(s)
		switch {
		case err != nil || !slices.Contains(watched, p):
			return nil, fmt.Errorf("expect: %q is not one of the prefixes", s)
		case len(asns) == 0 || len(asns) > maxOrigins || slices.Contains(asns, 0):
			return nil, fmt.Errorf("expect: %s needs 1 to %d ASNs, none of them 0", p, maxOrigins)
		}
		out[p] = asns
	}
	return out, nil
}

func (o *options) checkOrigins() error {
	if len(o.Origins) > maxOrigins {
		return fmt.Errorf("origins: %d named, at most %d", len(o.Origins), maxOrigins)
	}
	for i, asn := range o.Origins {
		if asn == 0 || slices.Contains(o.Origins[:i], asn) {
			return fmt.Errorf("origins: AS%d is 0 or named twice", asn)
		}
	}
	return nil
}

func (o *options) checkScopes() error {
	if len(o.Hosts) > maxScopes || len(o.Peers) > maxScopes {
		return fmt.Errorf("hosts and peers: at most %d each", maxScopes)
	}
	for _, h := range o.Hosts {
		if !collector.MatchString(h) {
			return fmt.Errorf("host %q must be a RIS collector, such as rrc00", h)
		}
	}
	for _, p := range o.Peers {
		if _, err := netip.ParseAddr(p); err != nil {
			return fmt.Errorf("peer %q must be an IP address", p)
		}
	}
	return nil
}

func (o *options) checkTypes() error {
	if len(o.Types) == 0 {
		return errors.New("types: name UPDATE, and RIS_PEER_STATE if wanted")
	}
	for i, t := range o.Types {
		if t != typeUpdate && t != typePeerState || slices.Contains(o.Types[:i], t) {
			return fmt.Errorf("types: %q is not UPDATE or RIS_PEER_STATE, or is named twice", t)
		}
	}
	return nil
}
