package client

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func seedTierRollback(t *testing.T, entries map[string]TierEntry) []Host {
	t.Helper()
	ownLockFiles(t, time.Millisecond, time.Second)
	hosts := make([]Host, 0, len(entries))
	for name := range entries {
		hosts = append(hosts, Host{Name: name, SSH: name})
	}
	if err := SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: entries}); err != nil {
		t.Fatal(err)
	}
	return hosts
}

func tierEntryForTest(name string, tier Tier) TierEntry {
	return TierEntry{Name: name, Tier: tier, SetAt: nowUTC(), HostID: strings.Repeat("1", 32)}
}

func pendingTier(t *testing.T, ctx context.Context, hosts []Host, name string, tier Tier) func() {
	t.Helper()
	rollback, err := mutateTiersWithRollback(ctx, hosts, func(ts *Tiers) error {
		e := ts.entries[name]
		e.Tier = tier
		ts.entries[name] = e
		return nil
	})
	if err != nil || rollback == nil {
		t.Fatalf("prepare tier change: rollback present=%v, err=%v", rollback != nil, err)
	}
	return rollback
}

func TestTierRollbackKeepsConcurrentHostIDDowngrades(t *testing.T) {
	for _, changed := range []string{"alpha", "beta"} {
		t.Run(changed, func(t *testing.T) {
			entries := map[string]TierEntry{
				"alpha": tierEntryForTest("alpha", TierOwn),
				"beta":  tierEntryForTest("beta", TierOwn),
			}
			hosts := seedTierRollback(t, entries)
			rollback := pendingTier(t, context.Background(), hosts, "alpha", TierThrowaway)
			laterMigration := nowUTC().Add(time.Hour)
			if err := mutateTiers(context.Background(), hosts, func(ts *Tiers) error {
				ts.Migrated = laterMigration
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if changed, err := noteHostID(context.Background(), Host{Name: changed, SSH: changed}, strings.Repeat("2", 32)); err != nil || !changed {
				t.Fatalf("concurrent host-ID change: changed=%v err=%v", changed, err)
			}
			rollback()
			got, err := readTiersFile()
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := got.Entry(changed)
			if entry.Tier != TierShared || !entry.IDChanged || entry.HostID != strings.Repeat("2", 32) {
				t.Fatalf("rollback lost concurrent host-ID downgrade: %+v", entry)
			}
			if changed == "beta" && got.Of("alpha") != TierOwn {
				t.Fatalf("rollback did not restore alpha: %s", got.Of("alpha"))
			}
			if !got.Migrated.Equal(laterMigration) {
				t.Fatalf("rollback replaced current migration metadata: %v", got.Migrated)
			}
		})
	}
}

func TestTierRollbackUsesTOMLTimestampEquality(t *testing.T) {
	before := tierEntryForTest("box", TierOwn)
	hosts := seedTierRollback(t, map[string]TierEntry{"box": before})
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", 0))
	rollback, err := mutateTiersWithRollback(context.Background(), hosts, func(ts *Tiers) error {
		e := ts.entries["box"]
		e.Tier, e.SetAt = TierShared, stamp
		ts.entries["box"] = e
		return nil
	})
	if err != nil || rollback == nil {
		t.Fatalf("prepare timestamp change: rollback=%v err=%v", rollback != nil, err)
	}
	written, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	after, _ := written.Entry("box")
	if !after.SetAt.Equal(stamp) || after.SetAt.Location() == stamp.Location() {
		t.Fatal("fixture did not round-trip an equal instant with a different time representation")
	}
	rollback()
	got, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := got.Entry("box")
	if !sameTierEntry(entry, before) {
		t.Fatalf("rollback failed across TOML timestamp conversion: %+v", entry)
	}
}

func TestTierRollbackChecksPresenceAsWellAsValue(t *testing.T) {
	for _, tc := range []struct {
		name              string
		before, after     bool
		replace, keepLive bool
	}{
		{name: "add", after: true},
		{name: "remove", before: true},
		{name: "remove then replace", before: true, replace: true, keepLive: true},
		{name: "add then delete", after: true, replace: true},
		{name: "change then delete", before: true, after: true, replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := map[string]TierEntry{}
			if tc.before {
				entries["box"] = tierEntryForTest("box", TierOwn)
			}
			hosts := seedTierRollback(t, entries)
			rollback, err := mutateTiersWithRollback(context.Background(), hosts, func(ts *Tiers) error {
				if tc.after {
					ts.entries["box"] = tierEntryForTest("box", TierShared)
				} else {
					delete(ts.entries, "box")
				}
				return nil
			})
			if err != nil || rollback == nil {
				t.Fatalf("prepare presence change: %v", err)
			}
			if tc.replace {
				if err := mutateTiers(context.Background(), hosts, func(ts *Tiers) error {
					if tc.keepLive {
						ts.entries["box"] = tierEntryForTest("box", TierThrowaway)
					} else {
						delete(ts.entries, "box")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			rollback()
			got, err := readTiersFile()
			if err != nil {
				t.Fatal(err)
			}
			wantHas := tc.before
			if tc.replace {
				wantHas = tc.keepLive
			}
			if got.Has("box") != wantHas {
				t.Fatalf("rollback changed newer presence: has=%v want=%v", got.Has("box"), wantHas)
			}
			if tc.keepLive && got.Of("box") != TierThrowaway {
				t.Fatalf("rollback changed newer value: %s", got.Of("box"))
			}
		})
	}
}

func TestUpdateHostsRollsBackTiersAfterCancellationOrFailedWrite(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel before save", true: "failed write"}[failWrite], func(t *testing.T) {
			before := tierEntryForTest("box", TierOwn)
			hosts := seedTierRollback(t, map[string]TierEntry{"box": before})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("save failed")
			oldBefore, oldWrite := beforeHostsSave, writeHosts
			t.Cleanup(func() { beforeHostsSave, writeHosts = oldBefore, oldWrite })
			want := context.Canceled
			if failWrite {
				writeHosts = func([]Host) error { cancel(); return failure }
				want = failure
			} else {
				beforeHostsSave = cancel
			}
			err := UpdateHosts(ctx, HostChange{Kind: "host set", Was: &hosts[0], Now: &hosts[0], Tier: TierShared})
			if !errors.Is(err, want) {
				t.Fatalf("failed host save: %v", err)
			}
			got, err := readTiersFile()
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := got.Entry("box")
			if !sameTierEntry(entry, before) {
				t.Fatalf("cancelled host save did not restore tier: %+v", entry)
			}
		})
	}
}

func TestFailedHostForgetRestoresTierAndMissingPinRefusal(t *testing.T) {
	entry := tierEntryForTest("box", TierOwn)
	key := newKey(t, "ssh-ed25519")
	entry.Key = key.Fingerprint()
	hosts := seedTierRollback(t, map[string]TierEntry{"box": entry})
	if err := writeKeys(pinPath("box"), "box", []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	oldWrite := writeHosts
	writeHosts = func([]Host) error { return errors.New("save failed") }
	t.Cleanup(func() { writeHosts = oldWrite })
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &hosts[0]}); err == nil {
		t.Fatal("host forget ignored failed hosts save")
	}
	got, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := got.Entry("box")
	if !sameTierEntry(kept, entry) || HasPin("box") {
		t.Fatalf("failed forget: tier=%+v pin present=%v", kept, HasPin("box"))
	}
	var user *UserError
	if err := checkTierKeyPin(context.Background(), "box"); !errors.As(err, &user) || user.Code != "host_key_unreadable" {
		t.Fatalf("failed forget lost its missing-pin refusal: %v", err)
	}
}

func TestTierRollbackKeepsMalformedLiveFile(t *testing.T) {
	hosts := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})
	rollback := pendingTier(t, context.Background(), hosts, "box", TierShared)
	bad := "[[host\n"
	if err := os.WriteFile(tiersPath(), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	rollback()
	got, err := os.ReadFile(tiersPath())
	if err != nil || string(got) != bad {
		t.Fatalf("rollback overwrote unreadable live file: %q %v", got, err)
	}
}

func TestTierRollbackHasABoundedLockWaitAfterCancel(t *testing.T) {
	hosts := seedTierRollback(t, map[string]TierEntry{"box": tierEntryForTest("box", TierOwn)})
	hostsWait = 100 * time.Millisecond
	notes := &noteLine{got: make(chan struct{})}
	ctx, cancel := context.WithCancel(WithNotes(context.Background(), notes))
	rollback := pendingTier(t, ctx, hosts, "box", TierShared)
	held, err := lockTiersFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	cancel()
	done := make(chan struct{})
	go func() { rollback(); close(done) }()
	t.Cleanup(func() {
		held()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("rollback did not end after the fixture released its lock")
		}
	})
	select {
	case <-notes.got:
	case <-time.After(time.Second):
		t.Fatal("rollback lost the caller's wait notes or inherited its cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rollback exceeded the lock-wait bound")
	}
	got, err := readTiersFile()
	if err != nil || got.Of("box") != TierShared {
		t.Fatalf("rollback wrote without the held lock: tier=%s err=%v", got.Of("box"), err)
	}
}
