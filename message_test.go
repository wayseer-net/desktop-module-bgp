package bgp

import (
	"bufio"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixture is each line of testdata/stream.ndjson.
func fixture(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/stream.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, slices.Clone(sc.Bytes()))
	}
	return out
}

func TestEveryFixtureLineParses(t *testing.T) {
	for i, line := range fixture(t) {
		if _, err := parse(line); err != nil {
			t.Errorf("line %d: %v", i+1, err)
		}
	}
}

func TestAnUpdateParses(t *testing.T) {
	e, err := parse(fixture(t)[2])
	if err != nil {
		t.Fatal(err)
	}
	m := e.msg
	switch {
	case e.kind != kindMessage || m.kind != typeUpdate || m.id != "192.0.2.2-0001":
		t.Errorf("kind %q %q id %q", e.kind, m.kind, m.id)
	case m.collector != "rrc00" || m.peer != netip.MustParseAddr("192.0.2.2") || m.peerASN != 64498:
		t.Errorf("from %s %s AS%d", m.collector, m.peer, m.peerASN)
	case !slices.Equal(m.path, []uint32{64498, 64499, 64500}) || m.origin() != 64500:
		t.Errorf("path %v origin %d, want prepends collapsed", m.path, m.origin())
	case len(m.announced) != 1 || m.announced[0] != netip.MustParsePrefix("203.0.113.0/24"):
		t.Errorf("announced %v", m.announced)
	case !m.at.Equal(time.UnixMilli(1791381453090)):
		t.Errorf("at %v", m.at)
	}
}

func TestAnASSetOfOneIsItsAS(t *testing.T) {
	e, err := parse(fixture(t)[4])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.msg.path, []uint32{65536, 65550}) || e.msg.origin() != 65550 {
		t.Errorf("path %v", e.msg.path)
	}
	e, err = parse([]byte(`{"type":"ris_message","data":{"type":"UPDATE","id":"x","host":"rrc00.ripe.net","peer":"192.0.2.1","peer_asn":"64496","path":[64496,[64497,64498]],"announcements":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.msg.path, []uint32{64496, 0}) || e.msg.origin() != 0 {
		t.Errorf("a wider set: path %v, origin %d", e.msg.path, e.msg.origin())
	}
}

func TestOtherMessagesAreKnownByType(t *testing.T) {
	for line, want := range map[string]string{
		`{"type":"pong","data":null}`:                        kindIgnored,
		`{"type":"ris_error","data":{"message":"too slow"}}`: kindError,
		`{"type":"ris_message","data":{"type":"KEEPALIVE","id":"x","host":"rrc00.ripe.net","peer":"192.0.2.1","peer_asn":"1"}}`: kindIgnored,
	} {
		e, err := parse([]byte(line))
		if err != nil || e.kind != want {
			t.Errorf("%s: %q, %v", line, e.kind, err)
		}
	}
	e, _ := parse([]byte(`{"type":"ris_error","data":{"message":"too slow"}}`))
	if e.err != "too slow" {
		t.Errorf("ris_error says %q", e.err)
	}
}

func TestBadMessagesAreErrors(t *testing.T) {
	const ok = `"type":"UPDATE","id":"x","host":"rrc00.ripe.net","peer":"192.0.2.1","peer_asn":"64496"`
	for _, line := range []string{
		`not json`,
		`{"type":"ris_message","data":"no"}`,
		`{"type":"ris_message","data":{"type":"UPDATE","id":"x","host":"","peer":"192.0.2.1","peer_asn":"64496"}}`,
		`{"type":"ris_message","data":{"type":"UPDATE","id":"x","host":"rrc00.ripe.net","peer":"nope","peer_asn":"64496"}}`,
		`{"type":"ris_message","data":{"type":"UPDATE","id":"x","host":"rrc00.ripe.net","peer":"192.0.2.1","peer_asn":"-1"}}`,
		`{"type":"ris_message","data":{` + ok + `,"path":["x"]}}`,
		`{"type":"ris_message","data":{` + ok + `,"path":[0]}}`,
		`{"type":"ris_message","data":{` + ok + `,"path":[[]]}}`,
		`{"type":"ris_message","data":{` + ok + `,"withdrawals":["nope"]}}`,
		`{"type":"ris_message","data":{` + ok + `,"announcements":[{"prefixes":["10.0.0.1/8"]}]}}`,
		`{"type":"ris_message","data":{` + ok + `,"path":[` + strings.Repeat("1,", maxPath) + `2]}}`,
	} {
		if _, err := parse([]byte(line)); err == nil {
			t.Errorf("%s accepted", line)
		}
	}
}
