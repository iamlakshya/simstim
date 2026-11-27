package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostPublicationDirectMetadataAPIAddsWithoutStateAuthority(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "metadata.example"}
	if err := SaveHostContext(t.Context(), h); err != nil {
		t.Fatal("direct metadata publication lost its established API:", err)
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, h) {
		t.Fatalf("the actual metadata CAS was not published: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name))); !os.IsNotExist(err) {
		t.Fatalf("metadata-only publication created a host JSON domain: %v", err)
	}
}

func TestHostPublicationScopeCanCreateOnlyNecessaryOP(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "metadata.example"}
	ctx, end, err := withHostPublicationObservation(t.Context(), HostChange{Kind: "host save", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	locked, unlock, err := LockHost(ctx, h.Name, "metadata publication")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, name := range hostStateFiles {
		called := false
		if err := hostObservationAttempt(locked, h, name, &called); err == nil || called {
			t.Fatalf("metadata publication acquired %s authority", name)
		}
	}
	checkedFixture := &Conn{Host: h, hostID: strings.Repeat("a", 32)}
	if err := checkedFixture.BindHostStateObservation(locked); err == nil {
		t.Fatal("metadata-only intent was upgraded into remote observation authority")
	}
	if _, err := os.Lstat(hostLockPath(h.Name)); err != nil {
		t.Fatal("the necessary admitted OP was not present:", err)
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name))); !os.IsNotExist(err) {
		t.Fatalf("publication-only scope created a JSON folder: %v", err)
	}
	unlock()
	if _, _, err := LockHost(ctx, h.Name, "stale publication"); err == nil {
		t.Fatal("a returned publication OP was reused after its physical lifetime ended")
	}
}

func TestHostPublicationDoesNotMasqueradeAsOwnedRegistration(t *testing.T) {
	stateFDTestHome(t)
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "registration.example"}
	ctx, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if err := UpdateHosts(ctx, HostChange{Kind: "host add", Now: &h, Pin: true}); err == nil {
		t.Fatal("an owned stage without its pre-capture token used metadata-only publication")
	}
	if _, err := FindHost(h.Name); err == nil {
		t.Fatal("the refused registration was published")
	}
}

func TestHostPublicationEndedOrDifferentGenerationCannotCreateOP(t *testing.T) {
	for _, endEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed-generation", true: "ended-scope"}[endEarly], func(t *testing.T) {
			stateFDTestHome(t)
			h := Host{Name: "box", SSH: "metadata.example"}
			ctx, end, err := withHostPublicationObservation(t.Context(), HostChange{Kind: "host save", Now: &h})
			if err != nil {
				t.Fatal(err)
			}
			defer end()
			if endEarly {
				end()
			} else if err := SaveHosts(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if _, unlock, err := LockHost(ctx, h.Name, "stale metadata"); err == nil {
				unlock()
				t.Fatal("a stale publication created an OP")
			}
			if _, err := os.Lstat(hostLockPath(h.Name)); !os.IsNotExist(err) {
				t.Fatalf("refused publication created an OP inode: %v", err)
			}
		})
	}
}

func TestHostPublicationWithoutCancelDoesNotReviveEndedMembershipScope(t *testing.T) {
	stateFDTestHome(t)
	var retained context.Context
	if err := withHostMembershipContext(t.Context(), func(locked context.Context) error {
		retained = context.WithoutCancel(locked)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := withHostMembershipContext(retained, func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatal("WithoutCancel revived a released native membership lease")
	}
}
