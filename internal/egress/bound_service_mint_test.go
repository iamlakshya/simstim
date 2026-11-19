package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The extension enters the real existing reservation/publication/ACK registry;
// test-only register callbacks in streaming oracles cannot prove this join.
func TestServiceMintLongFamiliesUseCanonicalDurablePublication(t *testing.T) {
	for _, fixture := range serviceMintFixtures() {
		if fixture.id != "pypi-upload-token" && fixture.id != "gitlab-pat-routable" && fixture.id != "gitlab-runner-authentication-token-routable" && fixture.id != "github-pat" && fixture.id != "npm-access-token" {
			continue
		}
		t.Run(fixture.id, func(t *testing.T) {
			f := newBoundControlFixture(t, false)
			if _, err := f.server.initialiseBoundControl(memberJSON(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))); err != nil {
				t.Fatal(err)
			}
			result := make(chan boundMintResult, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			go func() {
				sentinel, err := f.server.registerResponseSecret(ctx, "example.com:443", boundPrimaryUse(f.server, "KEY"), api.Secret(fixture.value), fixture.id)
				result <- boundMintResult{sentinel, err}
			}()
			event := f.nextMint(t)
			if event.State.Rule != fixture.id || event.Owner != "KEY" || len(event.State.Sentinel) != len(fixture.value) || event.State.Sentinel == api.Secret(fixture.value) {
				t.Fatal("source family lost its actual canonical shape/owner")
			}
			public, err := json.Marshal(event)
			if err != nil || bytes.Contains(public, []byte(fixture.value)) {
				t.Fatal("minted public receipt contained the real credential")
			}
			select {
			case <-result:
				t.Fatal("service credential escaped before durable ACK")
			default:
			}
			if err := f.acknowledge(event, true); err != nil {
				t.Fatal(err)
			}
			select {
			case completed := <-result:
				if completed.err != nil || completed.sentinel != event.State.Sentinel {
					t.Fatal("service mint did not finish its same exact reservation")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("service mint retained its ACK waiter")
			}
			view, err := f.server.responseSecretSnapshot(t.Context(), "example.com:443", boundPrimaryUse(f.server, "KEY"))
			if sentinel, ok := view.exact(api.Secret(fixture.value)); err != nil || !ok || sentinel != event.State.Sentinel {
				t.Fatal("ready canonical echo view did not include the service credential")
			}
			if _, err := f.server.registerResponseSecret(t.Context(), "other.example:443", boundPrimaryUse(f.server, "KEY"), api.Secret(fixture.value), fixture.id); err == nil {
				t.Fatal("service mint created cross-authority credential authority")
			}
		})
	}
}
