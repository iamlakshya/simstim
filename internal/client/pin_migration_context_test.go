package client

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestLegacyPinMigrationHonoursContext(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	k := newKey(t, "ssh-ed25519")
	old := "# A legacy pin.\n" + k.line(pinAlias(h.Name)) + "\n"
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, sshDir(), "known_hosts-box", old)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := migratePinContext(ctx, h.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration = %v", err)
	}
	assertLegacyUnchanged(t, h.Name, old)

	unlock, err := lockPinWait(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, op := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"Command", func(ctx context.Context) error {
			_, err := (&SSH{Host: h}).Command(ctx, "true")
			return err
		}},
		{"PinHostKey", func(ctx context.Context) error {
			_, err := PinHostKey(ctx, h, "", FirstUseAccept)
			return err
		}},
		{"Rekey", func(ctx context.Context) error {
			_, _, err := Rekey(ctx, h, "", nil)
			return err
		}},
	} {
		t.Run(op.name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- op.run(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("operation returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("migration ignored the operation's deadline")
			}
			assertLegacyUnchanged(t, h.Name, old)
		})
	}
}

func assertLegacyUnchanged(t *testing.T, name, want string) {
	t.Helper()
	if HasPin(name) {
		t.Fatal("migration published a pin after cancellation")
	}
	got, err := os.ReadFile(legacyPinPath(name))
	if err != nil || string(got) != want {
		t.Fatalf("legacy pin changed: %q, %v", got, err)
	}
}
