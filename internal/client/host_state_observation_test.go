package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostObservationFixture(t *testing.T) (Host, context.Context, *HostStateObservation, func()) {
	t.Helper()
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "original.example", Labels: map[string]string{"owner": "original"}, Repos: []string{"original.example/*"}}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	ctx, unlock, err := LockHost(t.Context(), h.Name, "observation fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	observation, err := ObserveHostStateContext(ctx, h, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(ctx, observation)
	// A private synthetic Conn isolates token/locking semantics. It does not
	// claim real SSH or helper hello provenance.
	bindGeneratedHostStateFixture(t, bound, h, strings.Repeat("a", 32))
	return h, bound, observation, unlock
}

func hostObservationAttempt(ctx context.Context, h Host, name string, called *bool) error {
	return withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, name, true, func([]byte, bool) ([]byte, error) {
		*called = true
		return []byte("owned fixture facts"), nil
	})
}

func TestHostObservationSameLogicalTokenCanUseFreshRuntimeContext(t *testing.T) {
	h, _, observation, _ := hostObservationFixture(t)
	ctx := WithHostStateObservation(t.Context(), observation)
	called := false
	if err := hostObservationAttempt(ctx, h, "sandboxes.json", &called); err != nil || !called {
		t.Fatalf("the same active owner was lost on a fresh runtime context: %v", err)
	}
}

func TestHostObservationExpiredTokenCannotAcquireReplacementOwner(t *testing.T) {
	h, _, old, unlock := hostObservationFixture(t)
	unlock()
	fresh, release, err := LockHost(t.Context(), h.Name, "replacement operation")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	called := false
	if err := hostObservationAttempt(WithHostStateObservation(fresh, old), h, "sandboxes.json", &called); err == nil || called {
		t.Fatal("an old token borrowed a replacement operation")
	}
	next, err := ObserveHostStateContext(fresh, h, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(fresh, next)
	bindGeneratedHostStateFixture(t, bound, h, strings.Repeat("a", 32))
	called = false
	if err := hostObservationAttempt(bound, h, "sandboxes.json", &called); err != nil || !called {
		t.Fatalf("a genuine new observation failed: %v", err)
	}
}

func TestHostObservationIdenticalMembershipABARefusesBeforeCreatingFolder(t *testing.T) {
	h, ctx, _, unlock := hostObservationFixture(t)
	unlock()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := hostObservationAttempt(ctx, h, "sandboxes.json", &called); err == nil || called {
		t.Fatal("identical remove and re-add revived the old observation")
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name))); !os.IsNotExist(err) {
		t.Fatalf("refused ABA admission created a host state folder: %v", err)
	}
}

func TestHostObservationUnrelatedAndIdenticalWritesInvalidateSource(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-content", true: "unrelated-host"}[unrelated], func(t *testing.T) {
			h, ctx, _, _ := hostObservationFixture(t)
			hosts := []Host{h}
			if unrelated {
				hosts = append(hosts, Host{Name: "other", SSH: "other.example"})
			}
			if err := SaveHosts(t.Context(), hosts); err != nil {
				t.Fatal(err)
			}
			called := false
			if err := hostObservationAttempt(ctx, h, "sandboxes.json", &called); err == nil || called {
				t.Fatal("a canonical membership write failed to invalidate the source")
			}
		})
	}
}

func TestHostObservationClonesOriginalMutableMetadata(t *testing.T) {
	h, _, observation, _ := hostObservationFixture(t)
	h.Labels["owner"] = "changed"
	h.Repos[0] = "changed.example/*"
	if observation.original.Labels["owner"] != "original" || observation.original.Repos[0] != "original.example/*" {
		t.Fatal("the observation aliases caller-owned mutable host metadata")
	}
}

func TestHostObservationWrongDomainAndUnscopedReaderCannotCreateSentinel(t *testing.T) {
	h, ctx, _, _ := hostObservationFixture(t)
	for _, test := range []struct {
		ctx context.Context
		h   Host
	}{
		{t.Context(), h},
		{ctx, Host{Name: "other", SSH: "other.example"}},
	} {
		called := false
		err := withStateFile(test.ctx, []string{"hosts", HostFileKey(test.h.Name)}, "sandboxes.json", false, func([]byte, bool) ([]byte, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatal("an unscoped or cross-domain reader reached its callback")
		}
		if _, err := os.Lstat(filepath.Join(StateDir(), "hosts", HostFileKey(test.h.Name))); !os.IsNotExist(err) {
			t.Fatalf("refused read created the domain or its sentinel: %v", err)
		}
	}
}

func TestHostObservationRegistrationKeepsActualStageAndLimitsStateRoles(t *testing.T) {
	stateFDTestHome(t)
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "new-box", SSH: "new.example"}
	ctx, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	observation, err := ObserveHostRegistrationContext(ctx, HostChange{Kind: "host add", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(ctx, observation)
	called := false
	if err := hostObservationAttempt(bound, h, "baseline.json", &called); err != nil || !called {
		t.Fatalf("a genuine pending registration could not save its baseline: %v", err)
	}
	called = false
	if err := hostObservationAttempt(bound, h, "sandboxes.json", &called); err == nil || called {
		t.Fatal("staged registration acquired sandbox grant authority")
	}
	abort()
	called = false
	if err := hostObservationAttempt(WithHostStateObservation(t.Context(), observation), h, "root-steps.json", &called); err == nil || called {
		t.Fatal("closing the owned stage did not expire its observation")
	}
}

func TestHostObservationRegistrationRefusesGlobalOrReplacedStage(t *testing.T) {
	stateFDTestHome(t)
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "new-box", SSH: "new.example"}
	ctx, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if _, err := ObserveHostRegistrationContext(t.Context(), HostChange{Now: &h}); err == nil {
		t.Fatal("a global stage substituted for the explicit owned binding")
	}
	_, closeReplacement, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer closeReplacement()
	if _, err := ObserveHostRegistrationContext(ctx, HostChange{Now: &h}); err == nil {
		t.Fatal("a replaced stage minted an observation")
	}
}

func TestHostObservationCheckedIdentityCannotChange(t *testing.T) {
	_, ctx, observation, _ := hostObservationFixture(t)
	// This internal Conn fixture proves token identity immutability only.
	// It is not evidence of SSH or helper hello provenance.
	checked := observation.checkedConn
	if err := checked.BindHostStateObservation(ctx); err != nil {
		t.Fatal(err)
	}
	checked.hostID = strings.Repeat("b", 32)
	if err := checked.BindHostStateObservation(ctx); err == nil || observation.hostID != strings.Repeat("a", 32) {
		t.Fatal("a second identity rebound the same observation")
	}
}
