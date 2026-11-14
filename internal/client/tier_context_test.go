package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadTiersContextRefusesCancelledReadsAndMigration(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "migration", true: "existing"}[existing], func(t *testing.T) {
			ownLockFiles(t, time.Millisecond, time.Second)
			h := Host{Name: "box", SSH: "box"}
			if err := SaveHosts(context.Background(), []Host{h}); err != nil {
				t.Fatal(err)
			}
			key := newKey(t, "ssh-ed25519")
			legacy := legacyPinPath(h.Name)
			if err := writeKeys(legacy, h.Name, []hostKey{key}); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{}}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(legacy)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := LoadTiersContext(ctx, []Host{h}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled tier read: %v", err)
			}
			if err := ensureTiersMigrated(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled migration check: %v", err)
			}
			after, err := os.ReadFile(legacy)
			if err != nil || !bytes.Equal(before, after) || HasPin(h.Name) {
				t.Fatalf("cancelled read changed the legacy pin: %v", err)
			}
			if _, err := os.Stat(tiersPath()); !existing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled migration created tiers.toml: %v", err)
			}
		})
	}
}

func TestLoadTiersContextChecksCancellationBeforeMigrationSave(t *testing.T) {
	ownLockFiles(t, time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := beforeTierMigrationSave
	beforeTierMigrationSave = cancel
	t.Cleanup(func() { beforeTierMigrationSave = old })
	if _, err := LoadTiersContext(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("migration cancelled before save: %v", err)
	}
	if _, err := os.Stat(tiersPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled migration wrote tiers.toml: %v", err)
	}
}

func TestTierLoadAndPinGateCancelTheirLockWait(t *testing.T) {
	for _, gate := range []string{"load", "pin"} {
		t.Run(gate, func(t *testing.T) {
			ownLockFiles(t, time.Millisecond, 5*time.Second)
			h := Host{Name: "box", SSH: "box"}
			if err := SaveHosts(context.Background(), []Host{h}); err != nil {
				t.Fatal(err)
			}
			held, err := lockTiersFile(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held()
			notes := &noteLine{got: make(chan struct{})}
			ctx, cancel := context.WithCancel(WithNotes(context.Background(), notes))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				defer close(done)
				if gate == "pin" {
					done <- checkTierKeyPin(ctx, h.Name)
					return
				}
				_, err := LoadTiersContext(ctx, []Host{h})
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				held()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("tier gate did not end after the fixture released its lock")
				}
			})
			select {
			case <-notes.got:
			case <-time.After(2 * time.Second):
				t.Fatal("tier gate never waited for its lock")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled tier gate: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled tier gate kept waiting")
			}
		})
	}
}

func TestTierPinGateRefusesCancelledAndMalformedTiers(t *testing.T) {
	ownLockFiles(t, time.Millisecond, time.Second)
	h := Host{Name: "box", SSH: "box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkTierKeyPin(ctx, h.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pin gate: %v", err)
	}
	if err := os.WriteFile(tiersPath(), []byte("[[host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var user *UserError
	if err := checkTierKeyPin(context.Background(), h.Name); !errors.As(err, &user) || user.Code != "bad_config" {
		t.Fatalf("malformed tiers did not stop pin gate: %v", err)
	}
}

func TestPinSavedKeepsBackgroundLockWaitSilent(t *testing.T) {
	ownLockFiles(t, time.Millisecond, time.Second)
	h := Host{Name: "box", SSH: "box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	key := newKey(t, "ssh-ed25519")
	if err := writeKeys(pinPath(h.Name), h.Name, []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	held, err := lockTiersFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	oldTake := takeLock
	waiting := make(chan struct{})
	var once bool
	takeLock = func(f *os.File) error {
		err := oldTake(f)
		if errors.Is(err, errLockTaken) && strings.HasSuffix(f.Name(), "tiers.toml.lock") && !once {
			once = true
			close(waiting)
		}
		return err
	}
	t.Cleanup(func() { takeLock = oldTake })
	var notes, notices bytes.Buffer
	ctx, cancel := context.WithCancel(WithNotes(WithNotices(context.Background(), &notices), &notes))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- pinSaved(ctx, h, false, false)
	}()
	t.Cleanup(func() {
		cancel()
		held()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("background gate did not end after the fixture released its lock")
		}
	})
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("background pin gate never waited")
	}
	// Let the held lock exceed hostsNote without relying on cancellation timing.
	time.Sleep(3 * hostsPoll)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("background pin gate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("background pin gate ignored cancellation")
	}
	if notes.Len() != 0 || notices.Len() != 0 {
		t.Fatalf("background gate wrote notes=%q notices=%q", notes.String(), notices.String())
	}
	if _, err := os.Stat(filepath.Join(ConfigDir(), "tiers.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled background gate migrated tiers: %v", err)
	}
}

func TestReconnectKeepsTierWarningsOffTheRawTerminal(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "box", Machine: &Machine{Provider: "tiertest", ID: "m1"}}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiers([]Host{h}); err != nil {
		t.Fatal(err)
	}
	old := currentBridge
	currentBridge = &hostIDBridge{hostID: strings.Repeat("1", 32)}
	t.Cleanup(func() { currentBridge = old })
	first, err := Dial(context.Background(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	currentBridge = &hostIDBridge{hostID: strings.Repeat("2", 32), refuseAttach: true}
	a := &attacher{conn: first, ssh: &SSH{Host: h}, o: AttachOptions{Host: h, Session: "abc12345", Out: out}}
	defer func() { a.conn.Close() }()
	var notes, notices, warnings bytes.Buffer
	ctx := WithWarnings(WithNotes(WithNotices(context.Background(), &notices), &notes), func(_, msg, hint string) {
		warnings.WriteString(msg + hint)
	})
	done, err := a.tryReconnect(ctx)
	var user *UserError
	if !done || !errors.As(err, &user) || user.Msg != "test attach ended" {
		t.Fatalf("fake reconnect did not reach its attach response: done=%v err=%v", done, err)
	}
	if notes.Len() != 0 || notices.Len() != 0 || warnings.Len() != 0 {
		t.Fatalf("reconnect injected client text: notes=%q notices=%q warnings=%q", notes.String(), notices.String(), warnings.String())
	}
	tiers, err := LoadTiers([]Host{h})
	if err != nil {
		t.Fatal(err)
	}
	if entry, _ := tiers.Entry(h.Name); entry.Tier != TierShared || !entry.IDChanged {
		t.Fatalf("silent reconnect lost the host-ID downgrade: %+v", entry)
	}
}
