package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestImageHostConstructorsRefuseInvalidAuthorityBeforeCreation(t *testing.T) {
	imageDomainHome(t)
	for _, domain := range []cachefs.Domain{{}, {Scope: strings.Repeat("a", 64)}, {HostID: strings.Repeat("b", 32)}, {Scope: strings.Repeat("A", 64), HostID: strings.Repeat("b", 32)}} {
		path := imageDomainPath(t, "never-created")
		if store, err := OpenHost(t.Context(), path, domain, imageDomainGate); err == nil {
			store.Close()
			t.Fatal("invalid domain created a host store")
		} else {
			requireImageDomainError(t, err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid authority reached filesystem creation")
		}
	}
	path := imageDomainPath(t, "missing-gate")
	_, err := OpenHost(t.Context(), path, imageDomainFixture("a", "b"), nil)
	requireImageDomainError(t, err)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing gate created a root")
	}
}

func TestImageHostExistingAndCancelledConstructorsCreateNothing(t *testing.T) {
	imageDomainHome(t)
	domain := imageDomainFixture("a", "b")
	path := imageDomainPath(t, "absent")
	if store, err := OpenHostExisting(t.Context(), path, domain); err == nil {
		store.Close()
		t.Fatal("existing-only constructor created a root")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	gate := func(context.Context, func() error) error { called = true; return nil }
	if _, err := OpenHost(ctx, path, domain, gate); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled constructor reached admission: %v, %v", err, called)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only or cancelled constructor left a namespace")
	}
}

func TestImageHostConstructorOwnsItsShortCreationGate(t *testing.T) {
	imageDomainHome(t)
	path := imageDomainPath(t, "images")
	calls := 0
	store, err := OpenHost(t.Context(), path, imageDomainFixture("a", "b"), func(_ context.Context, publish func() error) error {
		calls++
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("constructor mutated before its own gate")
		}
		if err := publish(); err != nil {
			return err
		}
		for _, name := range []string{"incoming", "ownership"} {
			if _, err := os.Stat(filepath.Join(path, name)); err != nil {
				t.Fatal("constructor callback omitted canonical infrastructure")
			}
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("constructor gate calls %d: %v", calls, err)
	}
	store.Close()
	failed := errors.New("post-callback native sync failed")
	if _, err := OpenHost(t.Context(), path, imageDomainFixture("a", "b"), func(_ context.Context, publish func() error) error {
		if err := publish(); err != nil {
			return err
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatalf("lost post-callback error: %v", err)
	}
	// A post-callback error is not namespace rollback permission.
	if _, err := os.Stat(filepath.Join(path, "ownership")); err != nil {
		t.Fatal("failed admission erased established infrastructure")
	}
}

func TestImageHostConstructorRejectsMissingOrRepeatedAdmissionCallback(t *testing.T) {
	imageDomainHome(t)
	for _, repeat := range []bool{false, true} {
		path := imageDomainPath(t, "images")
		_, err := OpenHost(t.Context(), path, imageDomainFixture("a", "b"), func(_ context.Context, publish func() error) error {
			if !repeat {
				return nil
			}
			if err := publish(); err != nil {
				return err
			}
			return publish()
		})
		requireImageDomainError(t, err)
		if !repeat {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("absent callback still created a root")
			}
		}
	}
}

func TestImageLeaseRolesAreClosedBeforeAnyCreation(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	for _, name := range []string{"arbitrary.lock", "agents-source.lock", "agents/nested/" + strings.Repeat("a", 64) + ".lock", "agents/" + strings.Repeat("A", 64) + ".lock", "agents/" + strings.Repeat("a", 64) + ".lock.lock", "incoming/nested/" + strings.Repeat("b", 64) + ".lock", strings.Repeat("A", 64) + ".lock", strings.Repeat("a", 64) + ".lock.lock"} {
		if lease, err := store.lock(t.Context(), name, false); err == nil {
			lease.Close()
			t.Fatalf("unreviewed resource role admitted: %s", name)
		}
	}
	source, err := store.lock(t.Context(), "agents.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.agentsRoot(t.Context(), true)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := errors.Join(root.Close(), source.Close()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{strings.Repeat("a", 64) + ".lock", "incoming/" + strings.Repeat("b", 64) + ".lock", "agents.lock", "agents/" + strings.Repeat("a", 64) + ".lock"} {
		lease, err := store.lock(t.Context(), name, false)
		if err != nil {
			t.Fatal(err)
		}
		lease.Close()
		receipt, err := store.readLeaseOwnership(leaseReceiptName(name))
		if err != nil || receipt.Domain == nil || *receipt.Domain != store.domain {
			t.Fatalf("canonical resource lost same domain: %+v, %v", receipt, err)
		}
	}
}
