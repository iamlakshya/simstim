package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneSDKStartRetiresOnlyItsNeverDispatchedReservation(t *testing.T) {
	c, _, probes := sdkStartSourceFixtureV1(t)
	// This actual helper feature gate runs after the SDK's complete private
	// preparation/reservation, before the physical new frame. No callback
	// supplies retirement authority or pretends a remote start was absent.
	_, err := c.NewSession(t.Context(), NewArgs{Agent: "shell", MakeFolder: true})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "helper_old" || !strings.Contains(refusal.Msg, "make folders") || *probes != 1 {
		t.Fatal("fixture did not reach the actual post-reservation pre-frame gate", err)
	}
	rows, err := SandboxPolicies(t.Context(), c.Host)
	if err != nil || len(rows) != 0 {
		t.Fatal("standalone SDK owner left its proven never-attempted reservation behind", len(rows), err)
	}
}

func TestStandaloneSDKCleanupKeepsOriginalContextAfterPublicationScopeEnds(t *testing.T) {
	c, _, probes := sdkStartSourceFixtureV1(t)
	path := filepath.Join(removalDir(c.Host.Name), "root-steps.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	// NoteSandboxStarted reads this actual file after beginGrant and the
	// publication scope, before the new frame. Cleanup must use its original
	// request budget after those shorter scopes have ended.
	_, err := c.NewSession(t.Context(), NewArgs{Agent: "shell"})
	if err == nil || !strings.Contains(err.Error(), "root-steps.json") || *probes != 1 {
		t.Fatal("fixture did not reach the actual post-publication-scope local gate", err)
	}
	rows, err := SandboxPolicies(t.Context(), c.Host)
	if err != nil || len(rows) != 0 {
		t.Fatal("standalone cleanup borrowed an ended publication scope", len(rows), err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "{" {
		t.Fatal("retiring one reservation changed unrelated damaged history", err)
	}
}
