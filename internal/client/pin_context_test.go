package client

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"sync"
	"testing"
	"time"
)

type pinContextOp struct {
	name    string
	current bool
	run     func(context.Context, Host) error
}

func pinContextOps() []pinContextOp {
	return []pinContextOp{
		{"migrate", false, func(ctx context.Context, h Host) error { return migratePinContext(ctx, h.Name) }},
		{"needs pin", false, func(ctx context.Context, h Host) error { _, err := needsPin(ctx, h); return err }},
		{"load tiers", false, func(ctx context.Context, h Host) error { _, err := loadTiers(ctx, []Host{h}); return err }},
		{"saved pin", false, func(ctx context.Context, h Host) error { return pinSaved(ctx, h, h, false) }},
		{"pin host", true, func(ctx context.Context, h Host) error {
			_, err := pinHostKey(ctx, h, "", FirstUseRefuse, false)
			return err
		}},
		{"rekey", true, func(ctx context.Context, h Host) error {
			_, _, err := Rekey(ctx, h, "", func(string, string) error { return nil })
			return err
		}},
		{"args", true, func(ctx context.Context, h Host) error {
			_, err := (&SSH{Host: h}).connArgs(ctx, true)
			return err
		}},
		{"answers", true, func(ctx context.Context, h Host) error { return answers(ctx, &SSH{Host: h}) }},
		{"doctor pin", false, func(ctx context.Context, h Host) error { _, _, err := PinCheckContext(ctx, h); return err }},
		{"command", true, func(ctx context.Context, h Host) error { _, err := (&SSH{Host: h}).Command(ctx, "true"); return err }},
		{"shell", true, func(ctx context.Context, h Host) error { _, err := (&SSH{Host: h}).Shell(ctx); return err }},
		{"login", true, loginOnce},
		{"verbose", true, func(ctx context.Context, h Host) error { _, err := (&SSH{Host: h}).VerboseLog(ctx); return err }},
		{"forward", true, func(ctx context.Context, h Host) error { _, err := (&SSH{Host: h}).Forward(ctx, nil); return err }},
		{"git", true, func(ctx context.Context, h Host) error { _, err := GitSSHCommand(ctx, h); return err }},
	}
}

func TestPinMigrationPreCancelledExistingPin(t *testing.T) {
	h, _, _ := legacyPinFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := migratePinContext(ctx, h.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("existing-pin fast path ignored cancellation: %v", err)
	}
}

func legacyPinFixture(t *testing.T, current bool) (Host, hostKey, []byte) {
	t.Helper()
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	key := newKey(t, "ssh-ed25519")
	path := legacyPinPath(h.Name)
	if current {
		path = pinPath(h.Name)
	}
	if err := writeKeys(path, h.Name, []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return h, key, data
}

func TestPinContextPreCancelledPathsLeaveLegacyPinAlone(t *testing.T) {
	for _, op := range pinContextOps() {
		t.Run(op.name, func(t *testing.T) {
			h, _, before := legacyPinFixture(t, false)
			files := tree(t, ConfigDir())
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := op.run(ctx, h); !errors.Is(err, context.Canceled) {
				t.Fatalf("pre-cancelled %s: %v", op.name, err)
			}
			after, err := os.ReadFile(legacyPinPath(h.Name))
			if err != nil || !bytes.Equal(before, after) || HasPin(h.Name) {
				t.Fatalf("pre-cancelled path changed its legacy pin: %v", err)
			}
			if after := tree(t, ConfigDir()); !maps.Equal(files, after) {
				t.Fatalf("pre-cancelled path wrote files: before=%v after=%v", files, after)
			}
		})
	}
}

func TestPinContextContendedPathsCancelPromptly(t *testing.T) {
	for _, current := range []bool{false, true} {
		name := map[bool]string{false: "legacy", true: "current"}[current]
		t.Run(name, func(t *testing.T) {
			for _, op := range pinContextOps() {
				if current && !op.current {
					continue
				}
				t.Run(op.name, func(t *testing.T) {
					h, _, _ := legacyPinFixture(t, current)
					held, err := lockPinAs(context.Background(), h.Name, true)
					if err != nil {
						t.Fatal(err)
					}
					defer held()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					old := pinLockWaiting
					waiting := make(chan struct{})
					var once sync.Once
					pinLockWaiting = func(name string) {
						if name == h.Name {
							once.Do(func() { close(waiting) })
						}
					}
					done := make(chan error, 1)
					go func() { defer close(done); done <- op.run(ctx, h) }()
					t.Cleanup(func() {
						cancel()
						held()
						select {
						case <-done:
						case <-time.After(time.Second):
							t.Error("operation stayed after its lock fixture ended")
						}
						pinLockWaiting = old
					})
					select {
					case <-waiting:
					case <-time.After(2 * time.Second):
						t.Fatal("operation did not reach the held pin lock")
					}
					cancel()
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("contended %s: %v", op.name, err)
						}
					case <-time.After(time.Second):
						t.Fatal("cancelled pin operation kept waiting")
					}
				})
			}
		})
	}
}

func TestPinLockCancelledBeforeFilesystemWrites(t *testing.T) {
	sshDirs(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := lockPinAs(ctx, "new", true); !errors.Is(err, context.Canceled) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("pre-cancelled pin lock: %v", err)
	}
	if _, err := os.Stat(sshDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled pin lock made its directory: %v", err)
	}
}

func TestPinLockCancelledAsAcquiredReleasesIt(t *testing.T) {
	sshDirs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := pinLockTaken
	pinLockTaken = func(string) { cancel() }
	t.Cleanup(func() { pinLockTaken = old })
	unlock, err := lockPinAs(ctx, "box", true)
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pin lock accepted a simultaneous cancellation: %v", err)
	}
	pinLockTaken = old
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err = lockPinAs(ctx, "box", true)
	if err != nil {
		t.Fatalf("cancelled lock kept ownership: %v", err)
	}
	unlock()
}
