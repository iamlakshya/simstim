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

func hostStateRemovalFixture(t *testing.T) (Host, context.Context, func(), string, map[string]func([]byte) error) {
	t.Helper()
	stateFDTestHome(t)
	original := Host{Name: "box", SSH: "original.example"}
	if err := SaveHosts(t.Context(), []Host{original}); err != nil {
		t.Fatal(err)
	}
	ctx, unlock, err := LockHost(t.Context(), original.Name, "host rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	ctx = stateFDObserveFixture(t, ctx, original)
	validators := map[string]func([]byte) error{}
	for _, name := range hostStateFiles {
		expected := []byte("owned:" + name)
		stateFDWriteContext(t, ctx, []string{"hosts", HostFileKey(original.Name)}, name, expected)
		validators[name] = func(raw []byte) error {
			if !bytes.Equal(raw, expected) {
				return errors.New("owner schema refused")
			}
			return nil
		}
	}
	folder := removalDir(original.Name)
	// Windows fixtures explicitly establish owner-only ACLs. No actual user
	// state or inherited public temp permission is cleanup authority.
	for _, path := range []string{StateDir(), filepath.Join(StateDir(), "hosts"), folder} {
		stateFDPrivatePath(t, path)
	}
	for _, name := range hostStateFiles {
		stateFDPrivatePath(t, filepath.Join(folder, name))
		stateFDPrivatePath(t, filepath.Join(folder, name+".lock"))
	}
	ctx, cancel, err := stateFDForgetIntent(t, ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	validators["removal.json"] = func(raw []byte) error { _, err := stateFDReceiptGeneration(raw); return err }
	if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &original}); err != nil {
		t.Fatal(err)
	}
	return original, ctx, unlock, folder, validators
}

func TestHostStateRemovalRetiresDomainsAndKeepsGlobalLedger(t *testing.T) {
	original, ctx, _, folder, validators := hostStateRemovalFixture(t)
	global := filepath.Join(StateDir(), grantsFileName)
	if err := os.WriteFile(global, []byte("global history"), 0600); err != nil {
		t.Fatal(err)
	}
	oldHook := beforeHostStateUnlink
	defer func() { beforeHostStateUnlink = oldHook }()
	var order []string
	beforeHostStateUnlink = func(name string) { order = append(order, name) }
	result, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators)
	result = result.FinalResult()
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), hostStateFiles[:]...)
	for _, name := range hostStateFiles {
		want = append(want, name+".lock")
	}
	want = append(want, HostFileKey(original.Name), filepath.Base(hostLockPath(original.Name))+".who", filepath.Base(hostLockPath(original.Name)))
	if !slices.Equal(order, want) {
		t.Fatalf("the durable journal/lock/folder/OP ordering changed: %q", order)
	}
	for _, name := range hostStateFiles {
		for _, path := range []string{filepath.Join(folder, name), filepath.Join(folder, name+".lock")} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || !slices.Contains(result.Removed, path) {
				t.Fatalf("a fully owned known domain was not actually retired: %s, %v", path, err)
			}
		}
	}
	for _, path := range []string{folder, hostLockPath(original.Name), hostLockPath(original.Name) + ".who"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || !slices.Contains(result.Removed, path) {
			t.Fatalf("a final domain was hidden as retained-only success: %s, %+v, %v", path, result, err)
		}
	}
	if len(result.Retained) != 0 || len(result.Preserved) != 0 {
		t.Fatalf("full retirement reported a leftover: %+v", result)
	}
	if got, err := os.ReadFile(global); err != nil || string(got) != "global history" {
		t.Fatalf("global ledger changed: %q, %v", got, err)
	}
}

func TestHostStateRemovalNeedsLiveLeaseAndAbsentSavedName(t *testing.T) {
	for _, kind := range []string{"no-lease", "ended-lease", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			original, ctx, unlock, folder, validators := hostStateRemovalFixture(t)
			switch kind {
			case "no-lease":
				ctx = t.Context()
			case "ended-lease":
				unlock()
			case "replacement":
				replacement := original
				replacement.SSH = "new.example"
				// Deliberate non-cooperative writer fixture: canonical bulk now
				// refuses the held actual OP instead of bypassing it.
				if err := writeHostsFile([]Host{replacement}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators); err == nil {
				t.Fatal("missing lease or replacement name was accepted")
			}
			for _, name := range hostStateFiles {
				if got, err := os.ReadFile(filepath.Join(folder, name)); err != nil || (name != "removal.json" && string(got) != "owned:"+name || name == "removal.json" && !strings.HasPrefix(string(got), "owned:removal.json:")) {
					t.Fatalf("refused cleanup changed %s: %q,%v", name, got, err)
				}
			}
		})
	}
}

func TestHostStateRemovalPreservesUnvalidatedRecordsAndJournal(t *testing.T) {
	for _, kind := range []string{"schema", "mode", "hardlink", "symlink", "missing-lock", "lock-mode"} {
		t.Run(kind, func(t *testing.T) {
			original, ctx, _, folder, validators := hostStateRemovalFixture(t)
			path := filepath.Join(folder, "baseline.json")
			foreign := filepath.Join(t.TempDir(), "foreign")
			if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "schema":
				if err := os.WriteFile(path, []byte("newer or malformed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, foreign+".link"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, path); err != nil {
					t.Fatal(err)
				}
			case "missing-lock":
				if err := os.Remove(path + ".lock"); err != nil {
					t.Fatal(err)
				}
			case "lock-mode":
				t.Cleanup(func() { _ = os.Chmod(path+".lock", 0600) })
				if err := os.Chmod(path+".lock", 0400); err != nil {
					t.Fatal(err)
				}
			}
			expected, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			preserved, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators)
			if err == nil || !slices.Contains(preserved.Preserved, path) {
				t.Fatalf("unvalidated record not reported: %+v,%v", preserved, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("unvalidated record removed: %v", err)
			}
			if got, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(got, expected) {
				t.Fatalf("unvalidated bytes changed: %q,%v", got, readErr)
			}
			if _, err := os.Stat(filepath.Join(folder, "removal.json")); err != nil {
				t.Fatalf("journal removed after incomplete cleanup: %v", err)
			}
			if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
				t.Fatalf("foreign link target changed: %q,%v", got, err)
			}
			if kind == "missing-lock" {
				if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("destructive path recreated a missing lock domain: %v", err)
				}
			}
		})
	}
}

func TestHostStateUnknownChildKeepsJournalAndReportsPartial(t *testing.T) {
	original, ctx, _, folder, validators := hostStateRemovalFixture(t)
	unknown := filepath.Join(folder, "future.json")
	if err := os.WriteFile(unknown, []byte("future kept"), 0600); err != nil {
		t.Fatal(err)
	}
	preserved, err := RemoveHostStateContext(ctx, original, strings.Repeat("a", 32), validators)
	if err == nil || !slices.Contains(preserved.Preserved, unknown) {
		t.Fatalf("unknown child was hidden as retained-only success: %+v,%v", preserved, err)
	}
	if got, err := os.ReadFile(unknown); err != nil || string(got) != "future kept" {
		t.Fatalf("unknown data changed: %q,%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(folder, "removal.json")); err != nil {
		t.Fatalf("unknown child lost retry journal: %v", err)
	}
}

// These opaque fixture receipts exercise engine ordering only. Production
// H15 supplies its single canonical identity/attempt decoder instead.
func stateFDReceiptGeneration(raw []byte) (HostForgetMembershipProof, error) {
	var proof HostForgetMembershipProof
	if string(raw) == "owned:removal.json" {
		return proof, nil
	}
	prefix := "owned:removal.json:"
	if !strings.HasPrefix(string(raw), prefix) {
		return proof, errors.New("fixture receipt changed owner")
	}
	parts := strings.Split(strings.TrimPrefix(string(raw), prefix), ":")
	if len(parts) != 2 {
		return proof, errors.New("fixture receipt lacks source-to-target proof")
	}
	proof.FromGeneration, proof.TargetGeneration = parts[0], parts[1]
	if !validHostForgetPair(proof) {
		return HostForgetMembershipProof{}, errors.New("fixture receipt pair invalid")
	}
	return proof, nil
}

func stateFDForgetHooks(original Host, hostID string) HostForgetReceiptHooks {
	return HostForgetReceiptHooks{
		Inspect: func(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
			identity := HostStateReceiptIdentity{Original: cloneObservationHost(original), HostID: hostID, Nonce: strings.Repeat("b", 32)}
			if name != original.Name {
				return identity, errors.New("fixture terminal receipt has another name")
			}
			raw, err := io.ReadAll(io.NewSectionReader(reader, 0, size))
			if err != nil {
				return identity, err
			}
			identity.Proof, err = stateFDReceiptGeneration(raw)
			if err == nil && !validHostForgetPair(identity.Proof) {
				err = errors.New("fixture terminal receipt has no complete pair")
			}
			if ctx.Err() != nil {
				return identity, ctx.Err()
			}
			return identity, err
		},
		Prepare: func(preparing context.Context, generation string) error {
			source, err := hostForgetPreparationSource(preparing, original, hostID, generation)
			if err != nil {
				return err
			}
			return withStateFile(preparing, []string{"hosts", HostFileKey(original.Name)}, "removal.json", true, func(raw []byte, exists bool) ([]byte, error) {
				if !exists {
					return nil, errors.New("fixture receipt missing")
				}
				prior, err := stateFDReceiptGeneration(raw)
				if err != nil {
					return nil, err
				}
				if prior.TargetGeneration != "" && (prior.FromGeneration != source || prior.TargetGeneration != generation) {
					return nil, errors.New("fixture prepared pair is immutable")
				}
				return []byte("owned:removal.json:" + source + ":" + generation), nil
			})
		},
		Verify: func(ctx context.Context, raw []byte) (HostForgetMembershipProof, error) {
			if err := ctx.Err(); err != nil {
				return HostForgetMembershipProof{}, err
			}
			return stateFDReceiptGeneration(raw)
		},
	}
}

func stateFDForgetIntent(t *testing.T, ctx context.Context, original Host) (context.Context, func(), error) {
	t.Helper()
	hostID := strings.Repeat("a", 32)
	return WithHostForgetIntent(ctx, original, hostID, stateFDForgetHooks(original, hostID))
}
