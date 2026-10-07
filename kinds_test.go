package bgp

import (
	"os"
	"slices"
	"strings"
	"testing"

	"wayseer.dev/sdk/manifest"
)

// TestManifestDeclaresEveryKind: the marketplace refuses a module that sends a kind outside
// its manifest.
func TestManifestDeclaresEveryKind(t *testing.T) {
	data, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := manifest.Fill(data, manifest.Platform{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	s := newTestState(t, fixtureOptions)
	for _, line := range fixture(t) {
		feed(t, s, t0, string(line))
	}
	ents, _ := s.world()
	for _, e := range ents {
		if !slices.ContainsFunc(m.Kinds, func(k manifest.Kind) bool { return k.Kind == string(e.Kind) }) {
			t.Errorf("manifest.yaml doesn't declare %s", e.Kind)
		}
	}
}
