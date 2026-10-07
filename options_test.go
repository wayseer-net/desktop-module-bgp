package bgp

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"wayseer.dev/sdk/sdktest"
)

// parsed decodes and checks options as Configure does.
func parsed(t *testing.T, yaml string) (options, watch, error) {
	t.Helper()
	cfg, err := sdktest.Config("bgp", yaml)
	if err != nil {
		t.Fatal(err)
	}
	o := defaults()
	if err := cfg.Decode(&o); err != nil {
		return o, watch{}, err
	}
	w, err := o.validate()
	return o, w, err
}

func TestDefaultsReadRISLiveForOnePrefix(t *testing.T) {
	o, w, err := parsed(t, "prefixes: [193.0.0.0/21]")
	if err != nil {
		t.Fatal(err)
	}
	if o.URL != "wss://ris-live.ripe.net/v1/ws/" || o.Client != "wayseer-bgp" || !o.MoreSpecific || o.LessSpecific {
		t.Errorf("defaults %+v", o)
	}
	if len(w.prefixes) != 1 || w.prefixes[0] != netip.MustParsePrefix("193.0.0.0/21") {
		t.Errorf("watch %+v", w)
	}
}

func TestExpectNamesLegitimateOrigins(t *testing.T) {
	_, w, err := parsed(t, "prefixes: [193.0.0.0/21, 2001:db8::/32]\nexpect: {193.0.0.0/21: [3333, 64496]}")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.expect[netip.MustParsePrefix("193.0.0.0/21")]; len(got) != 2 || got[0] != 3333 {
		t.Errorf("expect %v", w.expect)
	}
}

func TestBadOptionsAreRejected(t *testing.T) {
	for _, opts := range []string{
		"", // nothing watched: never the firehose
		"types: [RIS_PEER_STATE]\nprefixes: [193.0.0.0/21]", // no UPDATE, so nothing watched
		"prefixes: [not-a-prefix]",
		"prefixes: [193.0.0.1/21]", // not masked
		"prefixes: [10.0.0.0/7]",   // too wide with more-specifics
		"prefixes: [2000::/15]",
		"prefixes: [193.0.0.0/21, 193.0.0.0/21]",
		"origins: [0]",
		"origins: [3333, 3333]",
		"prefixes: [193.0.0.0/21]\nexpect: {10.0.0.0/8: [1]}", // not a watched prefix
		"prefixes: [193.0.0.0/21]\nexpect: {193.0.0.0/21: []}",
		"prefixes: [193.0.0.0/21]\nhosts: [example.com]",
		"prefixes: [193.0.0.0/21]\npeers: [not-an-ip]",
		"prefixes: [193.0.0.0/21]\ntypes: [KEEPALIVE]",
		"prefixes: [193.0.0.0/21]\ntypes: []",
		"prefixes: [193.0.0.0/21]\nurl: ftp://ris-live.ripe.net/",
		"prefixes: [193.0.0.0/21]\nurl: wss://user:pass@ris-live.ripe.net/v1/ws/",
		"prefixes: [193.0.0.0/21]\nclient: 'has spaces'",
		"prefixes: [193.0.0.0/21]\ninterval: 100ms",
		"prefixes: [193.0.0.0/21]\nstale_after: 1s",
		"prefixes: [193.0.0.0/21]\nmax_ases: 0",
		"prefixes: [193.0.0.0/21]\nmax_links: 10000000",
		"prefixes: [193.0.0.0/21]\nmax_prefixes: 0",
		"prefixes: [193.0.0.0/21]\nhosts: [rrc00, rrc01, rrc03, rrc04, rrc05, rrc06, rrc07, rrc10, rrc11]\npeers: [192.0.2.1, 192.0.2.2, 192.0.2.3, 192.0.2.4, 192.0.2.5, 192.0.2.6, 192.0.2.7, 192.0.2.8]",
	} {
		cfg, err := sdktest.Config("bgp", opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := New().Configure(context.Background(), cfg); err == nil {
			t.Errorf("%q accepted", opts)
		}
	}
}

func TestSubscriptionsAreAlwaysFiltered(t *testing.T) {
	o, _, err := parsed(t, "prefixes: [193.0.0.0/21, 2001:db8::/32]\norigins: [3333]\nhosts: [rrc00, rrc21]\nless_specific: true")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range subscriptions(&o) {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(b))
	}
	want := []string{
		`{"host":"rrc00","type":"UPDATE","prefix":["193.0.0.0/21","2001:db8::/32"],"moreSpecific":true,"lessSpecific":true}`,
		`{"host":"rrc00","type":"UPDATE","path":"3333$"}`,
		`{"host":"rrc00","type":"RIS_PEER_STATE"}`,
		`{"host":"rrc21","type":"UPDATE","prefix":["193.0.0.0/21","2001:db8::/32"],"moreSpecific":true,"lessSpecific":true}`,
		`{"host":"rrc21","type":"UPDATE","path":"3333$"}`,
		`{"host":"rrc21","type":"RIS_PEER_STATE"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("subscriptions\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
