package client

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func registerImageFixtureHosts(t *testing.T, names ...string) {
	t.Helper()
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for _, name := range names {
		known := false
		for _, host := range hosts {
			known = known || host.Name == name
		}
		if !known {
			hosts = append(hosts, Host{Name: name})
			changed = true
		}
	}
	if changed {
		if err := SaveHosts(t.Context(), hosts); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		sum := sha256.Sum256([]byte("image-fixture-host:" + name))
		identity := hex.EncodeToString(sum[:16])
		tiers, err := readTiersFile()
		if err != nil {
			t.Fatal(err)
		}
		if before, ok := tiers.Entry(name); ok && before.HostID == identity && tiers.Of(name) == TierOwn {
			continue
		}
		if err := SetTierContext(t.Context(), name, TierOwn, identity, ""); err != nil {
			t.Fatal(err)
		}
	}
}

// Use the real saved-source observation with the existing generated native
// transport fixture. Image tests still mock wire replies, never boot authority.
func bindImageFixturePeer(t *testing.T, c *Conn) {
	t.Helper()
	c.Features = append(c.Features, proto.FeatureImageTransferV1)
	registerImageFixtureHosts(t, c.Host.Name)
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if host.Name == c.Host.Name {
			c.Host = host
			break
		}
	}
	tiers, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := tiers.Entry(c.Host.Name)
	if !ok {
		t.Fatal("fixture host has no original tier identity")
	}
	c.hostID = entry.HostID
	c.proc, c.route = &hostStateGeneratedProc{}, route{t: &hostStateGeneratedTransport{}}
	owned, closeObservation, attempt, err := beginHostTransportObservation(t.Context(), c.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer closeObservation()
	if err := finishHostTransportObservation(owned, attempt, c); err != nil {
		t.Fatal(err)
	}
}
