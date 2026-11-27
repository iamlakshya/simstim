package client

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHostTerminalAbsentSelectionCreatesNoMembershipNamespace(t *testing.T) {
	for _, existingParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-state-parent", true: "existing-shared-parent"}[existingParent], func(t *testing.T) {
			stateFDTestHome(t)
			if existingParent {
				if err := os.MkdirAll(filepath.Join(StateDir(), "hosts"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			found, err := WithHostStateRetirementContext(t.Context(), "absent", func(context.Context, io.ReaderAt, int64, string) (HostStateReceiptIdentity, error) {
				called = true
				return HostStateReceiptIdentity{}, nil
			}, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
			if err != nil || found || called {
				t.Fatalf("read-only absence became proof: found=%v, called=%v, %v", found, called, err)
			}
			terminalAssertAbsent(t, ConfigDir(), hostsPath(), hostsPath()+".lock", hostLockPath("absent"), hostLockPath("absent")+".who", removalDir("absent"))
		})
	}
}

func TestHostTerminalExistingOPCannotCreateMissingMembershipLock(t *testing.T) {
	h, _, release, inspect, _ := hostTerminalFixture(t)
	release()
	path := hostsPath() + ".lock"
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(removalDir(h.Name), "removal.json"))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	found, err := WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
	if err == nil || found || called {
		t.Fatal("an absent membership lock inode was recreated as cleanup authority")
	}
	terminalAssertAbsent(t, path)
	if got, err := os.ReadFile(filepath.Join(removalDir(h.Name), "removal.json")); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("missing-lock refusal changed the receipt: %v", err)
	}
}

func TestHostTerminalExistingMembershipLockRechecksActualInodeAfterTake(t *testing.T) {
	h, _, release, inspect, _ := hostTerminalFixture(t)
	release()
	path := hostsPath() + ".lock"
	oldTake := takeLock
	defer func() { takeLock = oldTake }()
	changed := false
	foreign := []byte("foreign membership lock")
	takeLock = func(file *os.File) error {
		if filepath.Base(file.Name()) == filepath.Base(path) && !changed {
			changed = true
			if err := os.Rename(path, path+".old"); err != nil {
				return err
			}
			if err := os.WriteFile(path, foreign, 0600); err != nil {
				return err
			}
		}
		return oldTake(file)
	}
	called := false
	found, err := WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
	if !changed || err == nil || found || called {
		t.Fatalf("replaced existing member inode became authority: changed=%v, found=%v, called=%v, %v", changed, found, called, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign membership replacement was changed: %v", err)
	}
}

func TestHostTerminalCopiedCheckpointCannotIssueVerifiedScopeOnNewOPInode(t *testing.T) {
	h, ctx, release, inspect, validators := hostTerminalFixture(t)
	_, data := hostTerminalCheckpointFixture(t, h, ctx, validators)
	release()
	path := hostLockPath(h.Name)
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	inspected, called := false, false
	found, err := WithHostStateRetirementContext(t.Context(), h.Name, func(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
		inspected = true
		return inspect(ctx, reader, size, name)
	}, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
	if err == nil || found || inspected || called {
		t.Fatalf("copied bytes acquired native ownership: found=%v, inspected=%v, callback=%v, %v", found, inspected, called, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("foreign replacement OP was removed or replaced: %v", err)
	}
}
