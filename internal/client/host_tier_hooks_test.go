package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func assertHostFileLockHeld(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockExclusive(f); !errors.Is(err, errLockTaken) {
		t.Errorf("transaction hook ran without holding %s: %v", path, err)
	}
}

func TestHostFileHooksHoldTheirLockUntilTheHostSaveFinishes(t *testing.T) {
	for _, cause := range []string{"commit", "change-error", "cancel", "write-error", "cleanup-error", "cleanup-cancel"} {
		t.Run(cause, func(t *testing.T) {
			ownLockFiles(t, time.Millisecond, time.Second)
			if err := SaveHosts(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("save failed")
			oldBefore, oldWrite := beforeHostsSave, writeHosts
			t.Cleanup(func() { beforeHostsSave, writeHosts = oldBefore, oldWrite })
			if cause == "cancel" {
				beforeHostsSave = cancel
			}
			if cause == "write-error" {
				writeHosts = func([]Host) error { cancel(); return failure }
			}
			var events []string
			err := updateHostsFile(ctx, func(hosts []Host) ([]Host, hostFileHooks, error) {
				unlock, err := lockTiersFile(ctx)
				if err != nil {
					return nil, hostFileHooks{}, err
				}
				check := func(event string) {
					assertHostFileLockHeld(t, hostsPath()+".lock")
					assertHostFileLockHeld(t, tiersPath()+".lock")
					events = append(events, event)
				}
				hooks := hostFileHooks{
					rollback: func() { check("rollback") },
					commit:   func() { check("commit") },
					release:  func() { check("release"); unlock() },
					beforeSave: func() error {
						check("before-save")
						if cause == "cleanup-error" {
							return failure
						}
						if cause == "cleanup-cancel" {
							cancel()
						}
						return nil
					},
				}
				if cause == "change-error" {
					return nil, hooks, failure
				}
				return hosts, hooks, nil
			})
			want := "before-save,commit,release"
			switch cause {
			case "change-error", "cancel":
				want = "rollback,release"
			case "write-error", "cleanup-error", "cleanup-cancel":
				want = "before-save,rollback,release"
			}
			if strings.Join(events, ",") != want {
				t.Fatalf("hooks = %v, want %s", events, want)
			}
			if cause == "commit" && err != nil || cause != "commit" && err == nil {
				t.Fatalf("transaction result: %v", err)
			}
			unlock, err := lockTiersFile(context.Background())
			if err != nil {
				t.Fatal("tier lock leaked: ", err)
			}
			unlock()
		})
	}
}

func TestHostTierTransactionSerialisesConcurrentDowngrades(t *testing.T) {
	for _, target := range []string{"alpha", "beta"} {
		for _, abort := range []bool{false, true} {
			t.Run(target+map[bool]string{false: "/commit", true: "/cancel"}[abort], func(t *testing.T) {
				hosts := seedTierRollback(t, map[string]TierEntry{
					"alpha": tierEntryForTest("alpha", TierOwn),
					"beta":  tierEntryForTest("beta", TierOwn),
				})
				var alpha, observed Host
				for _, h := range hosts {
					if h.Name == "alpha" {
						alpha = h
					}
					if h.Name == target {
						observed = h
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				workerCtx, stopWorker := context.WithTimeout(context.Background(), 3*time.Second)
				defer stopWorker()
				waiting := make(chan struct{})
				var once sync.Once
				oldTake := takeLock
				takeLock = func(f *os.File) error {
					err := oldTake(f)
					if f.Name() == hostsPath()+".lock" && errors.Is(err, errLockTaken) {
						once.Do(func() { close(waiting) })
					}
					return err
				}
				t.Cleanup(func() { takeLock = oldTake })
				done := make(chan error, 1)
				oldBefore := beforeHostsSave
				beforeHostsSave = func() {
					go func() {
						_, err := noteHostID(workerCtx, observed, strings.Repeat("2", 32))
						done <- err
					}()
					if target == "alpha" {
						select {
						case err := <-done:
							var user *UserError
							if !errors.As(err, &user) || user.Code != "host_busy" {
								t.Errorf("same-host writer bypassed HostLock: %v", err)
							}
							done <- err
						case <-time.After(time.Second):
							t.Error("same-host writer did not refuse promptly")
						}
					} else {
						select {
						case <-waiting:
						case err := <-done:
							t.Errorf("tier writer was not blocked through the host save: %v", err)
							done <- err
						case <-time.After(time.Second):
							t.Error("tier writer did not reach the held hosts lock")
						}
					}
					if abort {
						cancel()
					}
				}
				t.Cleanup(func() { beforeHostsSave = oldBefore })
				err := UpdateHosts(ctx, HostChange{Kind: "host set", Was: &alpha, Now: &alpha, Tier: TierThrowaway})
				if abort && !errors.Is(err, context.Canceled) || !abort && err != nil {
					t.Fatalf("host save: %v", err)
				}
				select {
				case err := <-done:
					if target == "beta" && err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("tier writer did not finish after host transaction")
				}
				// The refused same-host observation can be retried once the
				// host command ends. It never publishes through that command.
				if target == "alpha" {
					if _, err := noteHostID(workerCtx, observed, strings.Repeat("2", 32)); err != nil {
						t.Fatal(err)
					}
				}
				got, err := readTiersFile()
				if err != nil {
					t.Fatal(err)
				}
				changed, _ := got.Entry(target)
				if changed.Tier != TierShared || !changed.IDChanged || changed.HostID != strings.Repeat("2", 32) {
					t.Fatalf("transaction lost the downgrade: %+v", changed)
				}
				if target == "beta" {
					want := TierThrowaway
					if abort {
						want = TierOwn
					}
					if got.Of("alpha") != want {
						t.Fatalf("alpha tier = %s, want %s", got.Of("alpha"), want)
					}
				}
			})
		}
	}
}

func guardedForgetFixture(t *testing.T, state string) (Host, string) {
	t.Helper()
	entry := tierEntryForTest("box", TierOwn)
	key := newKey(t, "ssh-ed25519")
	if state == "recorded" {
		entry.Key = key.Fingerprint()
	}
	hosts := seedTierRollback(t, map[string]TierEntry{"box": entry})
	if err := writeKeys(pinPath("box"), "box", []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	if state == "missing" {
		if err := os.Remove(tiersPath()); err != nil {
			t.Fatal(err)
		}
	}
	return hosts[0], key.Fingerprint()
}

func TestFailedForgetNeverLosesThePinnedKeyGuard(t *testing.T) {
	for _, state := range []string{"recorded", "empty-key", "missing"} {
		for _, cancelSave := range []bool{false, true} {
			t.Run(state+map[bool]string{false: "/write-error", true: "/cancel-after-cleanup"}[cancelSave], func(t *testing.T) {
				h, fingerprint := guardedForgetFixture(t, state)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				oldBefore, oldWrite := beforeHostsSave, writeHosts
				t.Cleanup(func() { beforeHostsSave, writeHosts = oldBefore, oldWrite })
				failure := errors.New("host write failed")
				want := failure
				if cancelSave {
					// Cancellation here is after actual cleanup. The saved
					// host must retain its key guard while publication fails.
					writeHosts = func([]Host) error { cancel(); return ctx.Err() }
					want = context.Canceled
				} else {
					writeHosts = func([]Host) error { return failure }
				}
				if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &h}); !errors.Is(err, want) {
					t.Fatalf("failed forget: %v", err)
				}
				if _, err := FindHost(h.Name); err != nil {
					t.Fatal("failed forget removed saved host: ", err)
				}
				got, err := readTiersFile()
				if err != nil {
					t.Fatal(err)
				}
				entry, _ := got.Entry(h.Name)
				if entry.Key != fingerprint || HasPin(h.Name) {
					t.Fatalf("failed forget lost guard: entry=%+v pin=%v", entry, HasPin(h.Name))
				}
				var user *UserError
				if err := checkTierKeyPin(context.Background(), h.Name); !errors.As(err, &user) || user.Code != "host_key_unreadable" {
					t.Fatalf("failed forget no longer refuses missing pin: %v", err)
				}
			})
		}
	}
}

func TestForgetGuardErrorsLeavePinAndWrapperUntouched(t *testing.T) {
	for _, state := range []string{"guard-write", "malformed"} {
		t.Run(state, func(t *testing.T) {
			h, fingerprint := guardedForgetFixture(t, "empty-key")
			wrapper := wrapperPath(h)
			if err := os.WriteFile(wrapper, []byte("keep this wrapper"), 0o600); err != nil {
				t.Fatal(err)
			}
			if state == "malformed" {
				if err := os.WriteFile(tiersPath(), []byte("[[host\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				old := writeHostTierFile
				writeHostTierFile = func(Tiers) error { return errors.New("tier guard write failed") }
				t.Cleanup(func() { writeHostTierFile = old })
			}
			if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &h}); err == nil {
				t.Fatal("forget ignored failed key guard")
			}
			pin, err := PinOf(h.Name)
			if err != nil || pin.Fingerprint != fingerprint {
				t.Fatalf("guard failure removed pin: %+v %v", pin, err)
			}
			if got, err := os.ReadFile(wrapper); err != nil || !bytes.Equal(got, []byte("keep this wrapper")) {
				t.Fatalf("guard failure removed wrapper: %q %v", got, err)
			}
		})
	}
}

func TestForgetOnlyWritesItsRecordedTierAfterCommit(t *testing.T) {
	h, fingerprint := guardedForgetFixture(t, "recorded")
	oldTier, oldBefore := writeHostTierFile, beforeHostsSave
	t.Cleanup(func() { writeHostTierFile, beforeHostsSave = oldTier, oldBefore })
	writes := 0
	writeHostTierFile = func(ts Tiers) error { writes++; return oldTier(ts) }
	beforeHostsSave = func() {
		if writes != 0 {
			t.Error("recorded guard was rewritten before host save")
		}
		got, err := readTiersFile()
		if err != nil || got.entries[h.Name].Key != fingerprint {
			t.Errorf("pre-commit forget removed key record: %v", err)
		}
		assertHostFileLockHeld(t, tiersPath()+".lock")
	}
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &h}); err != nil {
		t.Fatal(err)
	}
	got, err := readTiersFile()
	if err != nil || got.Has(h.Name) || writes != 1 {
		t.Fatalf("post-commit cleanup: entry=%v writes=%d err=%v", got.Has(h.Name), writes, err)
	}
}

func TestForgetFinishesTierCleanupAfterCommittedCancellation(t *testing.T) {
	h, _ := guardedForgetFixture(t, "recorded")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldWrite := writeHosts
	writeHosts = func(hosts []Host) error {
		err := oldWrite(hosts)
		if err == nil {
			cancel()
		}
		return err
	}
	t.Cleanup(func() { writeHosts = oldWrite })
	if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &h}); err != nil {
		t.Fatalf("committed forget: %v", err)
	}
	if _, err := FindHost(h.Name); err == nil {
		t.Fatal("committed forget retained host")
	}
	tiers, err := readTiersFile()
	if err != nil || tiers.Has(h.Name) {
		t.Fatalf("committed cancellation skipped tier cleanup: present=%v err=%v", tiers.Has(h.Name), err)
	}
}
