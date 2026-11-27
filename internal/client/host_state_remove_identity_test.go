package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHostStateRemovalRechecksSourceRootLockAndLease(t *testing.T) {
	for _, kind := range []string{"source", "root", "lock", "lease", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			original, ctx, unlock, folder, validators := hostStateRemovalFixture(t)
			active, cancel := context.WithCancel(ctx)
			defer cancel()
			old := beforeHostStateUnlink
			defer func() { beforeHostStateUnlink = old }()
			target := filepath.Join(folder, "baseline.json")
			changed := false
			var released chan struct{}
			beforeHostStateUnlink = func(name string) {
				if name != "baseline.json" || changed {
					return
				}
				changed = true
				switch kind {
				case "lease":
					// A different owner initiates closure; its physical close
					// must join this finaliser after it refuses the ended lease.
					released = make(chan struct{})
					go func() { unlock(); close(released) }()
					held := ctx.Value(heldKey{original.Name}).(*heldLock)
					hostStateLifetimeAwait(t, held.stateLifetime().ctx.Done())
					return
				case "cancel":
					cancel()
					return
				case "root":
					if err := os.Rename(folder, folder+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(folder, 0700); err != nil {
						t.Fatal(err)
					}
				case "lock":
					target += ".lock"
				}
				if kind != "root" {
					if err := os.Rename(target, target+".original"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(target, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			preserved, err := RemoveHostStateContext(active, original, strings.Repeat("a", 32), validators)
			if released != nil {
				hostStateLifetimeAwait(t, released)
			}
			if !changed || err == nil {
				t.Fatalf("changed authority was accepted: changed=%v err=%v", changed, err)
			}
			paths := preserved.Retained
			if kind == "root" {
				paths = preserved.Preserved
			}
			if !slices.Contains(paths, folder) {
				t.Fatalf("remaining or replaced root omitted: %+v", preserved)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cleanup lost parent cancellation: %v", err)
			}
			if kind == "source" || kind == "root" || kind == "lock" {
				if got, err := os.ReadFile(target); err != nil || string(got) != "foreign" {
					t.Fatalf("foreign replacement changed: %q,%v", got, err)
				}
			}
		})
	}
}

func TestHostStateRemovalRejectsIncompleteOwnerSetAndKeepsCaseTwin(t *testing.T) {
	original, ctx, _, folder, validators := hostStateRemovalFixture(t)
	twin := Host{Name: "BOX"}
	twinDir := removalDir(twin.Name)
	if twinDir == folder {
		t.Fatal("case-distinct hosts share a namespace")
	}
	staged, abort, err := StagePinOwned(t.Context(), twin.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	observation, err := ObserveHostRegistrationContext(staged, HostChange{Kind: "case twin fixture", Now: &twin})
	if err != nil {
		t.Fatal(err)
	}
	owned, release, err := LockHost(WithHostStateObservation(staged, observation), twin.Name, "case twin fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stateFDWriteContext(t, owned, []string{"hosts", HostFileKey(twin.Name)}, "baseline.json", []byte("case twin"))
	delete(validators, "sandboxes.json")
	if _, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators); err == nil {
		t.Fatal("missing owner validator was accepted")
	}
	if got, err := os.ReadFile(filepath.Join(twinDir, "baseline.json")); err != nil || string(got) != "case twin" {
		t.Fatalf("case twin changed: %q,%v", got, err)
	}
	for _, name := range hostStateFiles {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("partial validator set removed data: %v", err)
		}
	}
}
