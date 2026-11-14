package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The opaque engine fixture supplies no production JSON grammar. H15's real
// adapter separately proves complete receipt, pin, machine and stage semantics.
func hostTerminalFixture(t *testing.T) (Host, context.Context, func(), HostStateReceiptInspector, map[string]func([]byte) error) {
	t.Helper()
	h, ctx, _, unlock := hostObservationFixture(t)
	validators := map[string]func([]byte) error{}
	for _, name := range hostStateFiles {
		data := []byte("owned:" + name)
		if err := withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, name, true, func([]byte, bool) ([]byte, error) { return data, nil }); err != nil {
			t.Fatal(err)
		}
		validators[name] = func(raw []byte) error {
			if !bytes.Equal(raw, data) {
				return errors.New("generated owner fixture refused")
			}
			return nil
		}
	}
	for _, path := range []string{StateDir(), filepath.Join(StateDir(), "hosts"), removalDir(h.Name)} {
		stateFDPrivatePath(t, path)
	}
	for _, name := range hostStateFiles {
		stateFDPrivatePath(t, filepath.Join(removalDir(h.Name), name))
		stateFDPrivatePath(t, filepath.Join(removalDir(h.Name), name+".lock"))
	}
	inspect := HostStateReceiptInspector(func(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
		identity := HostStateReceiptIdentity{Original: cloneObservationHost(h), HostID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32)}
		if name != h.Name {
			return identity, errors.New("generated inspector has another name")
		}
		raw, err := io.ReadAll(io.NewSectionReader(reader, 0, size))
		if err != nil {
			return identity, err
		}
		identity.Proof, err = stateFDReceiptGeneration(raw)
		if err == nil && !validHostForgetPair(identity.Proof) {
			err = errors.New("generated inspector requires a complete prepared pair")
		}
		if stopped := ctx.Err(); stopped != nil {
			return identity, stopped
		}
		return identity, err
	})
	hooks := stateFDForgetHooks(h, strings.Repeat("a", 32))
	hooks.Inspect = inspect
	bound, cancel, err := WithHostForgetIntent(ctx, h, strings.Repeat("a", 32), hooks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	validators["removal.json"] = func(raw []byte) error { _, err := stateFDReceiptGeneration(raw); return err }
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &h}); err != nil {
		t.Fatal(err)
	}
	return h, bound, unlock, inspect, validators
}

func terminalAssertAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("terminal path remains: %s: %v", path, err)
		}
	}
}

func terminalRetryFixture(t *testing.T, h Host, inspect HostStateReceiptInspector, validators map[string]func([]byte) error) HostStateRemovalResult {
	t.Helper()
	var local HostStateRemovalResult
	found, err := WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(verified context.Context, identity HostStateReceiptIdentity) error {
		if !SameEntry(identity.Original, h) || identity.HostID != strings.Repeat("a", 32) {
			t.Fatal("existing-only retry changed its exact original owner")
		}
		var err error
		local, err = RemoveHostStateContext(verified, identity.Original, identity.HostID, validators)
		return err
	})
	local = local.FinalResult()
	if !found || err != nil || len(local.Retained) != 0 || len(local.Preserved) != 0 {
		t.Fatalf("proved terminal retry did not finish: found=%v, result=%+v, error=%v", found, local, err)
	}
	return local
}

func TestHostTerminalOuterEpilogueRevokesViewsClosesNativeHandlesAndUnlinksOPLast(t *testing.T) {
	h, ctx, unlock, _, validators := hostTerminalFixture(t)
	global := filepath.Join(StateDir(), grantsFileName)
	if err := os.WriteFile(global, []byte("global history retained"), 0600); err != nil {
		t.Fatal(err)
	}
	oldUnlink, oldValidate := beforeHostStateUnlink, stateValidateDescriptor
	defer func() { beforeHostStateUnlink, stateValidateDescriptor = oldUnlink, oldValidate }()
	var descriptors []*os.File
	stateValidateDescriptor = func(file *os.File, mode os.FileMode) (os.FileInfo, error) {
		descriptors = append(descriptors, file)
		return oldValidate(file, mode)
	}
	var order []string
	beforeHostStateUnlink = func(name string) {
		order = append(order, name)
		if name == HostFileKey(h.Name) {
			for _, file := range descriptors {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("a canonical transaction handle remained open at folder rmdir: %v", err)
				}
			}
		}
	}
	var local HostStateRemovalResult
	var escaped io.ReaderAt
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		err := withStateFileFD(verified, []string{"hosts", HostFileKey(h.Name)}, "baseline.json", false, func(view stateFileView) (stateFileReplace, error) {
			escaped = view.ReaderAt
			return nil, nil
		})
		if err != nil {
			return err
		}
		local, err = RemoveHostStateContext(verified, h, strings.Repeat("a", 32), validators)
		if err != nil {
			return err
		}
		if len(local.FinalResult().Retained) == 0 || len(order) != 0 {
			t.Fatal("nested removal claimed or performed terminal cleanup before its outer callback returned")
		}
		for _, name := range hostStateFiles {
			if _, err := os.Lstat(filepath.Join(removalDir(h.Name), name)); err != nil {
				t.Fatalf("a known source was deleted before the terminal epilogue: %v", err)
			}
		}
		return nil
	})
	local = local.FinalResult()
	if err != nil || len(local.Retained) != 0 || len(local.Preserved) != 0 {
		t.Fatalf("terminal finalisation incomplete: %+v, %v", local, err)
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("a view escaped terminal revocation: %v", err)
	}
	want := append([]string(nil), hostStateFiles[:]...)
	for _, name := range hostStateFiles {
		want = append(want, name+".lock")
	}
	want = append(want, HostFileKey(h.Name), filepath.Base(hostLockPath(h.Name))+".who", filepath.Base(hostLockPath(h.Name)))
	if !slices.Equal(order, want) {
		t.Fatalf("OP was not the last destructive effect: %q", order)
	}
	terminalAssertAbsent(t, removalDir(h.Name), hostLockPath(h.Name), hostLockPath(h.Name)+".who")
	unlock()
	if got, err := os.ReadFile(global); err != nil || string(got) != "global history retained" {
		t.Fatalf("terminal cleanup changed the global ledger: %q, %v", got, err)
	}
}

func TestHostTerminalEveryNativeBoundaryKeepsDurableCheckpointAndResumes(t *testing.T) {
	for index := 0; index < 11; index++ {
		t.Run(strings.Repeat("x", index+1), func(t *testing.T) {
			h, owner, unlock, inspect, validators := hostTerminalFixture(t)
			ctx, cancel := context.WithCancel(owner)
			defer cancel()
			old := beforeHostStateUnlink
			defer func() { beforeHostStateUnlink = old }()
			calls := 0
			beforeHostStateUnlink = func(string) {
				if calls == index {
					cancel()
				}
				calls++
			}
			local, err := RemoveHostStateContext(ctx, h, strings.Repeat("a", 32), validators)
			local = local.FinalResult()
			if !errors.Is(err, context.Canceled) || !slices.Contains(local.Retained, hostLockPath(h.Name)) {
				t.Fatalf("boundary %d lost cancellation or checkpoint: %+v, %v", index, local, err)
			}
			checkpoint, err := os.ReadFile(hostLockPath(h.Name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readHostTerminalFrame(t.Context(), bytes.NewReader(checkpoint), int64(len(checkpoint))); err != nil {
				t.Fatalf("boundary %d lost complete immutable proof: %v", index, err)
			}
			unlock()
			beforeHostStateUnlink = func(string) {}
			terminalRetryFixture(t, h, inspect, validators)
			terminalAssertAbsent(t, removalDir(h.Name), hostLockPath(h.Name), hostLockPath(h.Name)+".who")
		})
	}
}

func TestHostTerminalEmptyOPRetryUsesSourceWithoutCreatingDiagnostic(t *testing.T) {
	h, _, unlock, inspect, validators := hostTerminalFixture(t)
	unlock()
	terminalAssertAbsent(t, hostLockPath(h.Name)+".who")
	local := terminalRetryFixture(t, h, inspect, validators)
	if slices.Contains(local.Removed, hostLockPath(h.Name)+".who") {
		t.Fatal("existing-only retry fabricated and then removed a diagnostic")
	}
}

func TestHostTerminalAbsentRetryNeverClaimsPurgedOrCreatesHostState(t *testing.T) {
	stateFDTestHome(t)
	called := false
	found, err := WithHostStateRetirementContext(t.Context(), "box", func(context.Context, io.ReaderAt, int64, string) (HostStateReceiptIdentity, error) {
		called = true
		return HostStateReceiptIdentity{}, nil
	}, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
	if found || err != nil || called {
		t.Fatalf("absence supplied a receipt or completion: found=%v, called=%v, %v", found, called, err)
	}
	terminalAssertAbsent(t, removalDir("box"), hostLockPath("box"), hostLockPath("box")+".who")
}

func TestHostTerminalCallbackFailureKeepsEveryKnownNodeAndConcretePendingResult(t *testing.T) {
	h, ctx, _, _, validators := hostTerminalFixture(t)
	marker := errors.New("outer bookkeeping failed after preparation")
	var local HostStateRemovalResult
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		var err error
		local, err = RemoveHostStateContext(verified, h, strings.Repeat("a", 32), validators)
		if err != nil {
			return err
		}
		return marker
	})
	if err != marker || len(local.FinalResult().Retained) == 0 || len(local.FinalResult().Removed) != 0 {
		t.Fatalf("outer failure was hidden by premature completion: %+v, %v", local.FinalResult(), err)
	}
	for _, name := range hostStateFiles {
		if _, err := os.Lstat(filepath.Join(removalDir(h.Name), name)); err != nil {
			t.Fatalf("outer callback failure deleted %s: %v", name, err)
		}
	}
}
