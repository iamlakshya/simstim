package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostForgetBeforeFixture(t *testing.T) (Host, context.Context, func(), HostForgetReceiptHooks) {
	t.Helper()
	stateFDTestHome(t)
	original := Host{Name: "box", SSH: "original.example"}
	if err := writeHostsFile([]Host{original}); err != nil {
		t.Fatal(err)
	}
	ctx, unlock, err := LockHost(t.Context(), original.Name, "host rm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	ctx = stateFDObserveFixture(t, ctx, original)
	stateFDWriteContext(t, ctx, []string{"hosts", HostFileKey(original.Name)}, "removal.json", []byte("owned:removal.json"))
	hooks := stateFDForgetHooks(original, strings.Repeat("a", 32))
	return original, ctx, unlock, hooks
}

func bindHostForgetFixture(t *testing.T, ctx context.Context, original Host, hooks HostForgetReceiptHooks) context.Context {
	t.Helper()
	intent, cancel, err := WithHostForgetIntent(ctx, original, strings.Repeat("a", 32), hooks)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	return intent
}

func TestHostForgetPrepareFailureAndPrivateScopeCannotForgeCommit(t *testing.T) {
	original, ctx, _, hooks := hostForgetBeforeFixture(t)
	marker := errors.New("prepare refused")
	hooks.Prepare = func(ctx context.Context, target string) error {
		if _, err := hostForgetPreparationSource(ctx, original, strings.Repeat("a", 32), target); err != nil {
			return err
		}
		return marker
	}
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	if err := requireHostForgetPreparation(bound, original, strings.Repeat("a", 32), strings.Repeat("b", 32)); err == nil {
		t.Fatal("intent alone forged active comparison scope")
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); !errors.Is(err, marker) {
		t.Fatalf("prepare failure identity changed: %v", err)
	}
	after, err := os.ReadFile(hostsPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed preparation changed membership: %v", err)
	}
	called := false
	if err := WithHostForgetCommittedContext(bound, original, func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatal("failed preparation authorised local cleanup")
	}
}

func TestHostForgetPreparedWriteFailureReusesExactPair(t *testing.T) {
	original, ctx, _, hooks := hostForgetBeforeFixture(t)
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	writer := writeHostsSelectedGeneration
	defer func() { writeHostsSelectedGeneration = writer }()
	marker := errors.New("hosts write refused")
	writeHostsSelectedGeneration = func([]Host, string) error { return marker }
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); !errors.Is(err, marker) {
		t.Fatalf("publication failure: %v", err)
	}
	intent := bound.Value(hostForgetIntentKey{}).(*hostForgetIntent)
	prepared, err := readHostForgetReceipt(bound, intent)
	if err != nil || !validHostForgetPair(prepared) {
		t.Fatalf("durable preparation missing after write failure: %+v,%v", prepared, err)
	}
	source, err := currentMembershipGeneration(bound)
	if err != nil || source != prepared.FromGeneration {
		t.Fatalf("failed write committed target: %q,%v", source, err)
	}
	writeHostsSelectedGeneration = writer
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err != nil {
		t.Fatal(err)
	}
	current, err := currentMembershipGeneration(bound)
	if err != nil || current != prepared.TargetGeneration {
		t.Fatalf("retry refreshed or lost target: %q,%v", current, err)
	}
	var reread HostForgetMembershipProof
	err = WithHostForgetCommittedContext(bound, original, func(verified context.Context) error {
		var err error
		reread, err = readHostForgetReceipt(verified, intent)
		return err
	})
	if err != nil || reread != prepared {
		t.Fatalf("prepared pair changed on retry: %+v,%v", reread, err)
	}
}

func TestHostForgetRestartUsesDurablePairButNeverAbsence(t *testing.T) {
	original, ctx, unlock, hooks := hostForgetBeforeFixture(t)
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err != nil {
		t.Fatal(err)
	}
	unlock()
	called := false
	found, err := WithHostStateRetirementContext(t.Context(), original.Name, hooks.Inspect, func(verified context.Context, identity HostStateReceiptIdentity) error {
		return WithHostForgetCommittedContext(verified, identity.Original, func(context.Context) error { called = true; return nil })
	})
	if err != nil || !found || !called {
		t.Fatalf("matching existing-only post-forget proof could not resume: found=%v, called=%v error=%v", found, called, err)
	}
	if err := os.Remove(filepath.Join(removalDir(original.Name), "removal.json")); err != nil {
		t.Fatal(err)
	}
	called = false
	found, err = WithHostStateRetirementContext(t.Context(), original.Name, hooks.Inspect, func(context.Context, HostStateReceiptIdentity) error { called = true; return nil })
	if err == nil || found || called {
		t.Fatal("saved-name absence adopted a missing proof")
	}
}

func TestHostForgetABASameEntryAndUnrelatedWritesInvalidateProof(t *testing.T) {
	for _, kind := range []string{"identical-readd", "postforget-aba", "unrelated"} {
		t.Run(kind, func(t *testing.T) {
			original, ctx, _, hooks := hostForgetBeforeFixture(t)
			bound := bindHostForgetFixture(t, ctx, original, hooks)
			if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err != nil {
				t.Fatal(err)
			}
			receipt := filepath.Join(removalDir(original.Name), "removal.json")
			before, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unrelated" {
				if err := writeHostsFile([]Host{{Name: "another"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := writeHostsFile([]Host{original}); err != nil {
					t.Fatal(err)
				}
				if kind == "postforget-aba" {
					if err := writeHostsFile(nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "identical-readd" {
				if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err == nil {
					t.Fatal("same fields refreshed old prepared receipt after registration")
				}
			}
			called := false
			if err := WithHostForgetCommittedContext(bound, original, func(context.Context) error { called = true; return nil }); err == nil || called {
				t.Fatal("changed generation authorised cleanup or grant stamping")
			}
			after, err := os.ReadFile(receipt)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("stale pair was refreshed: %v", err)
			}
		})
	}
}

func TestHostForgetFirstLegacyIntentInitialisesUnchangedSource(t *testing.T) {
	stateFDTestHome(t)
	original := Host{Name: "box", SSH: "original.example"}
	if err := SaveHosts(t.Context(), []Host{original}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	rest := bytes.SplitN(raw, []byte("\n"), 2)[1]
	if err := os.WriteFile(hostsPath(), rest, 0600); err != nil {
		t.Fatal(err)
	}
	writer := writeHostsSelectedGeneration
	defer func() { writeHostsSelectedGeneration = writer }()
	var generations []string
	writeHostsSelectedGeneration = func(hosts []Host, generation string) error {
		if len(generations) == 0 && (len(hosts) != 1 || !SameEntry(hosts[0], original)) {
			t.Fatal("marker initialisation changed the saved list")
		}
		generations = append(generations, generation)
		return writer(hosts, generation)
	}
	// Upgrade migration now precedes observation, as the common canonical
	// OP admission requires. It cannot rewrite the source of an old payload.
	ctx, unlock, err := LockHost(t.Context(), original.Name, "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx = stateFDObserveFixture(t, ctx, original)
	stateFDWriteContext(t, ctx, []string{"hosts", HostFileKey(original.Name)}, "removal.json", []byte("owned:removal.json"))
	hooks := stateFDForgetHooks(original, strings.Repeat("a", 32))
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err != nil {
		t.Fatal(err)
	}
	var proof HostForgetMembershipProof
	err = WithHostForgetCommittedContext(bound, original, func(verified context.Context) error {
		var err error
		proof, err = readHostForgetReceipt(verified, bound.Value(hostForgetIntentKey{}).(*hostForgetIntent))
		return err
	})
	if err != nil || len(generations) != 2 || proof.FromGeneration != generations[0] || proof.TargetGeneration != generations[1] || generations[0] == generations[1] {
		t.Fatalf("legacy source/target publication wrong: %+v,%q,%v", proof, generations, err)
	}
}

func TestHostForgetVerifiedScopeCannotStampReplacementState(t *testing.T) {
	original, ctx, _, hooks := hostForgetBeforeFixture(t)
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err != nil {
		t.Fatal(err)
	}
	// Deliberate isolated raw fixture bytes, not an unobserved ledger writer.
	if err := os.WriteFile(filepath.Join(StateDir(), grantsFileName), []byte("active replacement history"), 0600); err != nil {
		t.Fatal(err)
	}
	old := beforeGrantsStateWrite
	defer func() { beforeGrantsStateWrite = old }()
	changed := false
	err := WithHostForgetCommittedContext(bound, original, func(verified context.Context) error {
		beforeGrantsStateWrite = func() {
			if changed {
				return
			}
			changed = true
			// This low-level fixture deliberately simulates an observed write
			// outside the held membership domain. Public SaveHosts would wait.
			replacement := original
			replacement.SSH = "replacement.example"
			if err := writeHostsFile([]Host{replacement}); err != nil {
				t.Fatal(err)
			}
		}
		return withStateFile(verified, nil, grantsFileName, true, func([]byte, bool) ([]byte, error) { return []byte("incorrectly marked removed"), nil })
	})
	if !changed || err == nil {
		t.Fatalf("replacement authority was not rechecked: changed=%v error=%v", changed, err)
	}
	if got, err := os.ReadFile(filepath.Join(StateDir(), grantsFileName)); err != nil || string(got) != "active replacement history" {
		t.Fatalf("replacement grants were stamped: %q,%v", got, err)
	}
}

func TestHostForgetPreparedUnknownSourceIsNeverRefreshed(t *testing.T) {
	original, ctx, _, hooks := hostForgetBeforeFixture(t)
	bound := bindHostForgetFixture(t, ctx, original, hooks)
	path := filepath.Join(removalDir(original.Name), "removal.json")
	target := strings.Repeat("c", 32)
	if err := os.WriteFile(path, []byte("owned:removal.json:"+target), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &original}); err == nil {
		t.Fatal("legacy target-only proof was adopted")
	}
	after, err := os.ReadFile(hostsPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("unknown-source preparation changed membership: %v", err)
	}
}
