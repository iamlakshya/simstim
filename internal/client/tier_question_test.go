package client

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func unansweredTierHost(t *testing.T) Host {
	t.Helper()
	fakeSSH(t)
	h := Host{Name: "box", Machine: &Machine{Provider: "tiertest", ID: "question-machine"}}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeTiersFile(NewTiers(nil)); err != nil {
		t.Fatal(err)
	}
	previous := currentBridge
	currentBridge = &hostIDBridge{hostID: strings.Repeat("1", 32)}
	t.Cleanup(func() { currentBridge = previous })
	return h
}

func TestIdentityBookkeepingKeepsAnUnansweredTier(t *testing.T) {
	h := unansweredTierHost(t)
	c, err := Dial(context.Background(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	fp := newKey(t, "ssh-ed25519").Fingerprint()
	for _, record := range []func() error{
		func() error { return recordPinnedKey(context.Background(), h, fp) },
		func() error { return recordRekey(context.Background(), h, fp) },
	} {
		if err := record(); err != nil {
			t.Fatal(err)
		}
		ts, err := readTiersFile()
		if err != nil || !ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierShared {
			t.Fatalf("bookkeeping answered the tier: pending=%v tier=%s err=%v", ts.NeedsAnswer(h.Name), ts.Of(h.Name), err)
		}
	}
	if err := SetTierContext(context.Background(), h.Name, TierOwn, "", ""); err != nil {
		t.Fatal(err)
	}
	ts, err := readTiersFile()
	if err != nil || ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierOwn {
		t.Fatalf("explicit answer was not recorded: pending=%v tier=%s err=%v", ts.NeedsAnswer(h.Name), ts.Of(h.Name), err)
	}
}

func TestFirstTierQuestionPersistsOnceWithoutHoldingPolicyLocks(t *testing.T) {
	h := unansweredTierHost(t)
	asked := 0
	ctx := WithTierQuestion(context.Background(), func(ctx context.Context, got Host) (Tier, error) {
		asked++
		if !SameEntry(got, h) {
			t.Fatal("question named another saved entry")
		}
		f, err := os.OpenFile(hostLockPath(h.Name), os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := lockExclusive(f); err != nil {
			_ = f.Close()
			t.Fatal("question held HostLock: ", err)
		}
		_ = f.Close()
		unlockHosts, err := lockHostsFile(ctx)
		if err != nil {
			t.Fatal("question held hosts lock: ", err)
		}
		unlockHosts()
		unlockTiers, err := lockTiersFile(ctx)
		if err != nil {
			t.Fatal("question held tiers lock: ", err)
		}
		unlockTiers()
		return TierOwn, nil
	})
	for range 2 {
		if got, err := ResolveHostTier(ctx, h); err != nil || got != TierOwn {
			t.Fatalf("first tier resolution = %s, %v", got, err)
		}
	}
	ts, err := readTiersFile()
	if err != nil || asked != 1 || ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierOwn {
		t.Fatalf("answer did not persist exactly once: asked=%d pending=%v tier=%s err=%v", asked, ts.NeedsAnswer(h.Name), ts.Of(h.Name), err)
	}
}

func TestFirstTierQuestionNeedsAVerifiedLiveHost(t *testing.T) {
	h := unansweredTierHost(t)
	currentBridge.hostID = "invalid"
	asked := false
	ctx := WithTierQuestion(context.Background(), func(context.Context, Host) (Tier, error) { asked = true; return TierOwn, nil })
	if tier, err := ResolveHostTier(ctx, h); err == nil || tier != TierShared || asked {
		t.Fatalf("unverified host reached its question: tier=%s asked=%v err=%v", tier, asked, err)
	}
	ts, err := readTiersFile()
	if err != nil || !ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierShared {
		t.Fatalf("unverified host acquired trust: pending=%v tier=%s err=%v", ts.NeedsAnswer(h.Name), ts.Of(h.Name), err)
	}
}

func TestFirstTierQuestionFailureNeverPublishesOwn(t *testing.T) {
	for _, failure := range []string{"cancel", "no answer", "write error", "saved entry changed"} {
		t.Run(failure, func(t *testing.T) {
			h := unansweredTierHost(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previousWrite := writeSavedTierFile
			t.Cleanup(func() { writeSavedTierFile = previousWrite })
			ctx = WithTierQuestion(ctx, func(context.Context, Host) (Tier, error) {
				switch failure {
				case "cancel":
					cancel()
				case "no answer":
					return TierUnknown, nil
				case "write error":
					writeSavedTierFile = func(Tiers) error { return errors.New("tier save failed") }
				case "saved entry changed":
					replacement := h
					replacement.Machine = &Machine{Provider: "tiertest", ID: "replacement"}
					if err := SaveHosts(context.Background(), []Host{replacement}); err != nil {
						t.Fatal(err)
					}
				}
				return TierOwn, nil
			})
			if got, err := ResolveHostTier(ctx, h); err == nil || got == TierOwn {
				t.Fatalf("failed answer granted own: tier=%s err=%v", got, err)
			}
			ts, err := readTiersFile()
			if err != nil || !ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierShared {
				t.Fatalf("failed answer was recorded: pending=%v tier=%s err=%v", ts.NeedsAnswer(h.Name), ts.Of(h.Name), err)
			}
		})
	}
}

func TestFirstTierQuestionKeepsAConcurrentExplicitAnswer(t *testing.T) {
	h := unansweredTierHost(t)
	ctx := WithTierQuestion(context.Background(), func(ctx context.Context, _ Host) (Tier, error) {
		if err := SetTierContext(ctx, h.Name, TierShared, "", ""); err != nil {
			t.Fatal(err)
		}
		return TierOwn, nil
	})
	if got, err := ResolveHostTier(ctx, h); err != nil || got != TierShared {
		t.Fatalf("question overwrote a concurrent answer: %s, %v", got, err)
	}
}

func TestFirstTierQuestionRefusesAnIdentityChangedDuringTheAnswer(t *testing.T) {
	h := unansweredTierHost(t)
	ctx := WithTierQuestion(context.Background(), func(ctx context.Context, _ Host) (Tier, error) {
		if _, err := noteHostID(ctx, h, strings.Repeat("2", 32)); err != nil {
			t.Fatal(err)
		}
		return TierOwn, nil
	})
	var user *UserError
	if got, err := ResolveHostTier(ctx, h); got != TierShared || !errors.As(err, &user) || user.Code != "host_changed" {
		t.Fatalf("stale tier answer: tier=%s err=%v", got, err)
	}
	ts, err := readTiersFile()
	if err != nil || !ts.NeedsAnswer(h.Name) || ts.Of(h.Name) != TierShared {
		t.Fatalf("stale answer granted trust: tier=%s pending=%v err=%v", ts.Of(h.Name), ts.NeedsAnswer(h.Name), err)
	}
}

func TestPendingTierCannotGrantItsStoredOwnLabel(t *testing.T) {
	ts := NewTiers(map[string]TierEntry{"box": {Tier: TierOwn, NeedsAnswer: true}})
	if ts.Of("box") != TierShared || !ts.NeedsAnswer("box") {
		t.Fatal("pending stored label granted own")
	}
}

func TestTierQuestionsStayOffNonInteractiveAndLockedPaths(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(map[bool]string{false: "no input", true: "publication lock"}[locked], func(t *testing.T) {
			h := unansweredTierHost(t)
			asked := false
			ctx := WithTierQuestion(context.Background(), func(context.Context, Host) (Tier, error) { asked = true; return TierOwn, nil })
			if locked {
				var unlock func()
				var err error
				ctx, unlock, err = LockHost(ctx, h.Name, "grant publication")
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			} else {
				ctx = WithoutInput(ctx)
			}
			if got, err := ResolveHostTier(ctx, h); err != nil || got != TierShared || asked {
				t.Fatalf("unsafe question path: tier=%s asked=%v err=%v", got, asked, err)
			}
		})
	}
}

func TestConcurrentTierUsesJoinOneQuestion(t *testing.T) {
	h := unansweredTierHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var asked atomic.Int32
	ctx = WithTierQuestion(ctx, func(ctx context.Context, _ Host) (Tier, error) {
		if asked.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return TierOwn, nil
		case <-ctx.Done():
			return TierShared, ctx.Err()
		}
	})
	done := make(chan error, 2)
	resolve := func() {
		got, err := ResolveHostTier(ctx, h)
		if err == nil && got != TierOwn {
			err = errors.New("wrong tier")
		}
		done <- err
	}
	go resolve()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("question never opened")
	}
	go resolve()
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("concurrent uses asked %d times", asked.Load())
	}
	if _, err := os.Stat(tiersPath()); err != nil {
		t.Fatal(err)
	}
}
