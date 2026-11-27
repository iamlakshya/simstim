package client

import (
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
)

func savedTierWriters(h Host, fp string) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"set":   func(ctx context.Context) error { return SetTierContext(ctx, h.Name, TierShared, "", "") },
		"pin":   func(ctx context.Context) error { return recordPinnedKey(ctx, h, fp) },
		"rekey": func(ctx context.Context) error { return recordRekey(ctx, h, fp) },
		"hello": func(ctx context.Context) error {
			_, err := noteHostID(ctx, h, strings.Repeat("2", 32))
			return err
		},
	}
}

func TestSavedTierWritersRespectHostLock(t *testing.T) {
	for _, writer := range []string{"set", "pin", "rekey", "hello"} {
		t.Run(writer, func(t *testing.T) {
			h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})[0]
			before, err := os.ReadFile(tiersPath())
			if err != nil {
				t.Fatal(err)
			}
			_, unlock, err := LockHost(context.Background(), h.Name, "host rm")
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			fp := newKey(t, "ssh-ed25519").Fingerprint()
			var user *UserError
			if err := savedTierWriters(h, fp)[writer](context.Background()); !errors.As(err, &user) || user.Code != "host_busy" {
				t.Fatalf("writer bypassed the host command: %v", err)
			}
			after, err := os.ReadFile(tiersPath())
			if err != nil || string(after) != string(before) {
				t.Fatalf("busy writer changed tiers: %v", err)
			}
		})
	}
}

func TestUnchangedHelloWorksWhileAHostCommandAsks(t *testing.T) {
	h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})[0]
	_, unlock, err := LockHost(context.Background(), h.Name, "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before, err := os.ReadFile(tiersPath())
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := noteHostID(context.Background(), h, strings.Repeat("1", 32)); err != nil || changed {
		t.Fatalf("unchanged hello: changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(tiersPath())
	if err != nil || string(after) != string(before) {
		t.Fatalf("unchanged hello wrote policy: %v", err)
	}
}

func TestSavedTierWritersCheckMembershipUnderTheHostFileLock(t *testing.T) {
	for _, writer := range []string{"set", "pin", "rekey", "hello"} {
		t.Run(writer, func(t *testing.T) {
			h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})[0]
			oldTake := takeLock
			dropped := false
			takeLock = func(f *os.File) error {
				if f.Name() == hostsPath()+".lock" && !dropped {
					dropped = true
					// A legacy writer removed the entry after the first read.
					if err := writeHosts(nil); err != nil {
						return err
					}
					if err := writeTiersFile(NewTiers(nil)); err != nil {
						return err
					}
				}
				return oldTake(f)
			}
			t.Cleanup(func() { takeLock = oldTake })
			fp := newKey(t, "ssh-ed25519").Fingerprint()
			var user *UserError
			if err := savedTierWriters(h, fp)[writer](context.Background()); !errors.As(err, &user) || user.Code != "host_changed" {
				t.Fatalf("writer revived a removed host: %v", err)
			}
			ts, err := readTiersFile()
			if err != nil || ts.Has(h.Name) || !dropped {
				t.Fatalf("removed host acquired a tier: has=%v checked=%v err=%v", ts.Has(h.Name), dropped, err)
			}
		})
	}
}

func TestObservedIdentityCannotChangeAReplacementHost(t *testing.T) {
	for _, writer := range []string{"pin", "rekey", "hello"} {
		t.Run(writer, func(t *testing.T) {
			h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierShared)})[0]
			replacement := h
			replacement.SSH = "another.example"
			if err := SaveHosts(context.Background(), []Host{replacement}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(tiersPath())
			if err != nil {
				t.Fatal(err)
			}
			fp := newKey(t, "ssh-ed25519").Fingerprint()
			var user *UserError
			if err := savedTierWriters(h, fp)[writer](context.Background()); !errors.As(err, &user) || user.Code != "host_changed" {
				t.Fatalf("stale identity reached replacement host: %v", err)
			}
			after, err := os.ReadFile(tiersPath())
			if err != nil || string(after) != string(before) {
				t.Fatalf("replacement host acquired stale identity: %v", err)
			}
		})
	}
}

func TestSavedTierWriteChecksCancellationAndBothLocks(t *testing.T) {
	h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before, err := os.ReadFile(tiersPath())
	if err != nil {
		t.Fatal(err)
	}
	err = mutateSavedHostTiers(ctx, h, "host set", func(ts *Tiers) error {
		assertHostFileLockHeld(t, hostLockPath(h.Name))
		assertHostFileLockHeld(t, hostsPath()+".lock")
		assertHostFileLockHeld(t, tiersPath()+".lock")
		e := ts.entries[h.Name]
		e.Tier = TierShared
		ts.entries[h.Name] = e
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled tier write: %v", err)
	}
	after, err := os.ReadFile(tiersPath())
	if err != nil || string(after) != string(before) {
		t.Fatalf("cancelled mutation reached the tier file: %v", err)
	}
}

func TestSetTierCannotCreateAnUnsavedHost(t *testing.T) {
	ownLockFiles(t, 0, 0)
	var user *UserError
	if err := SetTierContext(context.Background(), "missing", TierOwn, "", ""); !errors.As(err, &user) || user.Code != "host_not_found" {
		t.Fatalf("unsaved host tier: %v", err)
	}
	if _, err := os.Lstat(tiersPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsaved host created tiers: %v", err)
	}
}

func TestCancelledSavedTierWritersDoNotTouchState(t *testing.T) {
	for _, writer := range []string{"set", "pin", "rekey", "hello"} {
		t.Run(writer, func(t *testing.T) {
			h := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})[0]
			before := tree(t, ConfigDir())
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			fp := newKey(t, "ssh-ed25519").Fingerprint()
			if err := savedTierWriters(h, fp)[writer](ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled writer: %v", err)
			}
			if after := tree(t, ConfigDir()); !maps.Equal(before, after) {
				t.Fatalf("cancelled writer touched state: before=%v after=%v", before, after)
			}
		})
	}
}
