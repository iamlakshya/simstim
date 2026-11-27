package client

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestDefaultHostSurvivesTypedBulkUpdateAndForget(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("default = 'box'\n[[host]]\nname = 'box'\nssh = 'box.example'\n")
	if err := os.WriteFile(hostsPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	box := Host{Name: "box", SSH: "box.example"}
	other := Host{Name: "other", SSH: "other.example"}
	if err := SaveHosts(t.Context(), []Host{other, box}); err != nil {
		t.Fatal(err)
	}
	first, err := currentMembershipGeneration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertDefault := func() {
		t.Helper()
		got, err := savedDefaultHost()
		if err != nil || got != "box" {
			t.Fatalf("typed hosts writer lost saved default: %q, %v", got, err)
		}
	}
	assertDefault()
	changed := cloneObservationHost(box)
	changed.SSH = "new.example"
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host set", Was: &box, Now: &changed}); err != nil {
		t.Fatal(err)
	}
	second, err := currentMembershipGeneration(t.Context())
	if err != nil || first == second {
		t.Fatalf("typed default retention skipped fresh membership generation: %v", err)
	}
	assertDefault()
	if err := UpdateHosts(t.Context(), HostChange{Kind: "host rm", Was: &other}); err != nil {
		t.Fatal(err)
	}
	third, err := currentMembershipGeneration(t.Context())
	if err != nil || second == third {
		t.Fatalf("forget skipped the same generation writer: %v", err)
	}
	assertDefault()
}

func TestDefaultHostSurvivesActualRawPolicyWriter(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath(), []byte("default='box'\n[[host]]\nname='box'\nssh='old.example'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "old.example"}
	ctx, release, err := LockHost(t.Context(), h.Name, "policy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	original, err := os.ReadFile(hostsPath()) // AFTER original OP/legacy marker.
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(original, []byte("old.example"), []byte("new.example"), 1)
	next := cloneObservationHost(h)
	next.SSH = "new.example"
	if err := writeHostPolicyDocumentContext(ctx, h, []Host{next}, edited); err != nil {
		t.Fatal(err)
	}
	if got, err := savedDefaultHost(); err != nil || got != "box" {
		t.Fatalf("raw document writer lost saved default: %q, %v", got, err)
	}
}

func TestTypedHostWriterRefusesInvalidDefaultWithoutOverwriting(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("default={future='unknown'}\n[[host]]\nname='box'\nssh='box.example'\n")
	if err := os.WriteFile(hostsPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeHostsFileGeneration([]Host{{Name: "box", SSH: "other.example"}}, strings.Repeat("a", 32)); err == nil {
		t.Fatal("writer ignored the canonical default type/alias guard")
	}
	if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, original) {
		t.Fatal("an invalid current default was overwritten")
	}
}
