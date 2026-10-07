package bgp_test

import (
	"os"
	"strings"
	"testing"

	"wayseer.dev/sdk/manifest"
)

// TestManifestNamesTheModule keeps manifest.yaml as the marketplace wants it: this id, version
// and namespace, no actions, and no keyring.
func TestManifestNamesTheModule(t *testing.T) {
	m := readManifest(t)
	if m.ID != "wayseer-labs/bgp" || m.Namespace != "bgp" || m.Version != "0.1.0" {
		t.Errorf("manifest.yaml names %s %s in %s", m.ID, m.Version, m.Namespace)
	}
	if len(m.Actions) != 0 || m.Secrets {
		t.Errorf("manifest.yaml declares actions %v or secrets %v", m.Actions, m.Secrets)
	}
}

// readManifest reads manifest.yaml as dev sign does, filling the fields it writes.
func readManifest(t *testing.T) manifest.Manifest {
	t.Helper()
	data, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := manifest.Fill(data, manifest.Platform{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
