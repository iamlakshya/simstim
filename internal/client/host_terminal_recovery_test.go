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

func hostTerminalCheckpointFixture(t *testing.T, h Host, ctx context.Context, validators map[string]func([]byte) error) (hostTerminalFrame, []byte) {
	t.Helper()
	marker := errors.New("stop after complete checkpoint before epilogue")
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		if _, err := RemoveHostStateContext(verified, h, strings.Repeat("a", 32), validators); err != nil {
			return err
		}
		return marker
	})
	if err != marker {
		t.Fatalf("checkpoint fixture did not stop at its exact boundary: %v", err)
	}
	data, err := os.ReadFile(hostLockPath(h.Name))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := readHostTerminalFrame(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return frame, data
}

func TestHostTerminalFullHeaderTornReceiptAndFooterRecoverExactSuffix(t *testing.T) {
	for _, atFooter := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "footer"}[atFooter], func(t *testing.T) {
			h, ctx, unlock, inspect, validators := hostTerminalFixture(t)
			frame, data := hostTerminalCheckpointFixture(t, h, ctx, validators)
			length := frame.receiptStart + frame.header.ReceiptSize/2
			if atFooter {
				length = frame.total - 3
			}
			// This intentional private fixture tear changes only the same OP
			// contents. The source, native inventory and diagnostic stay exact.
			if err := os.Truncate(hostLockPath(h.Name), length); err != nil {
				t.Fatal(err)
			}
			unlock()
			got, err := os.ReadFile(hostLockPath(h.Name))
			if err != nil || !bytes.Equal(got, data[:length]) {
				t.Fatalf("error unwind changed the exact prefix: %v", err)
			}
			terminalRetryFixture(t, h, inspect, validators)
		})
	}
}

func TestHostTerminalTornHeaderMissingSourceChangedInventoryAndUnknownChildRefuse(t *testing.T) {
	for _, kind := range []string{"header", "missing-source", "changed-data", "changed-lock", "changed-who", "unknown-child", "changed-prefix"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, unlock, inspect, validators := hostTerminalFixture(t)
			frame, data := hostTerminalCheckpointFixture(t, h, ctx, validators)
			length := frame.receiptStart + 1
			if kind == "header" {
				length = frame.receiptStart - 1
			}
			if err := os.Truncate(hostLockPath(h.Name), length); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(removalDir(h.Name), "baseline.json")
			switch kind {
			case "missing-source":
				if err := os.Remove(filepath.Join(removalDir(h.Name), "removal.json")); err != nil {
					t.Fatal(err)
				}
			case "changed-data", "changed-lock", "changed-who":
				if kind == "changed-lock" {
					path += ".lock"
				}
				if kind == "changed-who" {
					path = hostLockPath(h.Name) + ".who"
				}
				if err := os.Rename(path, filepath.Join(t.TempDir(), "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("owned:baseline.json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-child":
				path = filepath.Join(removalDir(h.Name), "future.json")
				if err := os.WriteFile(path, []byte("future kept"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-prefix":
				changed := append([]byte(nil), data[:length]...)
				changed[len(changed)-1] ^= 1
				if err := os.WriteFile(hostLockPath(h.Name), changed, 0600); err != nil {
					t.Fatal(err)
				}
			}
			unlock()
			before, err := os.ReadFile(hostLockPath(h.Name))
			if err != nil {
				t.Fatal(err)
			}
			var result HostStateRemovalResult
			_, err = WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(verified context.Context, identity HostStateReceiptIdentity) error {
				var err error
				result, err = RemoveHostStateContext(verified, identity.Original, identity.HostID, validators)
				return err
			})
			if err == nil || len(result.FinalResult().Removed) != 0 {
				t.Fatalf("unproved torn state allowed cleanup: %+v, %v", result.FinalResult(), err)
			}
			if got, err := os.ReadFile(hostLockPath(h.Name)); err != nil || !bytes.Equal(got, before) {
				t.Fatalf("refusal truncated or changed the unproved prefix: %v", err)
			}
		})
	}
}

func TestHostTerminalCompleteCheckpointRefusesRecordedAbsentNodeAppearance(t *testing.T) {
	for _, kind := range []string{"data", "lock", "who"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, unlock, inspect, validators := hostTerminalFixture(t)
			path := filepath.Join(removalDir(h.Name), "baseline.json")
			if kind == "who" {
				path = hostLockPath(h.Name) + ".who"
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if kind == "lock" {
				path += ".lock"
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			_, checkpoint := hostTerminalCheckpointFixture(t, h, ctx, validators)
			contents := []byte("owned:baseline.json")
			if kind != "data" {
				contents = nil
			}
			if err := os.WriteFile(path, contents, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
			var result HostStateRemovalResult
			_, err = WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(verified context.Context, identity HostStateReceiptIdentity) error {
				var err error
				result, err = RemoveHostStateContext(verified, identity.Original, identity.HostID, validators)
				return err
			})
			if err == nil || len(result.FinalResult().Removed) != 0 {
				t.Fatalf("recorded-absent %s appearance authorised deletion: %+v, %v", kind, result.FinalResult(), err)
			}
			after, statErr := os.Stat(path)
			if statErr != nil || !os.SameFile(before, after) {
				t.Fatalf("appeared node was removed or replaced: %v", statErr)
			}
			if got, err := os.ReadFile(hostLockPath(h.Name)); err != nil || !bytes.Equal(got, checkpoint) {
				t.Fatalf("immutable checkpoint changed on refusal: %v", err)
			}
		})
	}
}

func TestHostTerminalChangedMembershipAndMissingOPNeverSupplyRetryAuthority(t *testing.T) {
	for _, kind := range []string{"target", "readded", "missing-op", "empty-no-source"} {
		t.Run(kind, func(t *testing.T) {
			h, _, unlock, inspect, _ := hostTerminalFixture(t)
			unlock()
			switch kind {
			case "target":
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			case "readded":
				// Deliberate old/non-cooperative writer fixture. Canonical bulk
				// refuses the active or terminal OP instead of bypassing it.
				if err := writeHostsFile([]Host{h}); err != nil {
					t.Fatal(err)
				}
			case "missing-op":
				if err := os.Remove(hostLockPath(h.Name)); err != nil {
					t.Fatal(err)
				}
			case "empty-no-source":
				if err := os.Remove(filepath.Join(removalDir(h.Name), "removal.json")); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			found, err := WithHostStateRetirementContext(t.Context(), h.Name, inspect, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
			if err == nil || found || called {
				t.Fatalf("unknown ownership became authority: found=%v, called=%v, %v", found, called, err)
			}
		})
	}
}

func TestHostTerminalKnownOwnerRetentionAndUnknownChildPreventCheckpointMutation(t *testing.T) {
	for _, kind := range []string{"retained", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, _, _, validators := hostTerminalFixture(t)
			path := filepath.Join(removalDir(h.Name), "baseline.json")
			if kind == "retained" {
				validators["baseline.json"] = func([]byte) error { return RetainHostState(errors.New("known foreign baseline history")) }
			} else {
				path = filepath.Join(removalDir(h.Name), "future.json")
				if err := os.WriteFile(path, []byte("future kept"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := RemoveHostStateContext(ctx, h, strings.Repeat("a", 32), validators)
			if err == nil || len(result.Removed) != 0 {
				t.Fatalf("retention or unknown child was ignored: %+v, %v", result, err)
			}
			paths := result.Preserved
			if kind == "retained" {
				paths = result.Retained
			}
			if !slices.Contains(paths, path) {
				t.Fatalf("incomplete node omitted from concrete outcome: %+v", result)
			}
			info, err := os.Stat(hostLockPath(h.Name))
			if err != nil || info.Size() != 0 {
				t.Fatalf("refused pre-delete inventory modified OP: %v", err)
			}
		})
	}
}

func TestHostTerminalCompleteRetrySyncsAndRereadsOwnerBeforeFirstDelete(t *testing.T) {
	h, ctx, unlock, inspect, validators := hostTerminalFixture(t)
	hostTerminalCheckpointFixture(t, h, ctx, validators)
	unlock()
	oldSync, oldUnlink := afterHostTerminalSync, beforeHostStateUnlink
	defer func() { afterHostTerminalSync, beforeHostStateUnlink = oldSync, oldUnlink }()
	synced, ownerAfterSync, deleted := false, false, false
	afterHostTerminalSync = func() { synced = true }
	wrapped := func(ctx context.Context, reader io.ReaderAt, size int64, name string) (HostStateReceiptIdentity, error) {
		if synced {
			ownerAfterSync = true
		}
		return inspect(ctx, reader, size, name)
	}
	beforeHostStateUnlink = func(string) {
		deleted = true
		if !synced || !ownerAfterSync {
			t.Fatal("a complete-looking footer bypassed this retry's real Sync and owner reread")
		}
	}
	terminalRetryFixture(t, h, wrapped, validators)
	if !synced || !ownerAfterSync || !deleted {
		t.Fatal("the actual complete retry omitted its durability/owner/deletion sequence")
	}
}
