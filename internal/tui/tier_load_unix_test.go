//go:build unix

package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
)

// runModelTierLoad uses the context owned by the model, as production does.
// The worker receives no separate context that could mask a lost model context.
func runModelTierLoad(m *Model, reload bool, done chan error) {
	defer close(done)
	if reload {
		done <- m.reloadHost("box")().(hostReloadedMsg).err
		return
	}
	loaded := New(m.ctx, m.be, Options{})
	err := loaded.loadErr
	loaded.Close()
	done <- err
}

func TestModelTierLoadingCancelsWhileItsLockIsHeld(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "reload"}[reload], func(t *testing.T) {
			ownFiles(t)
			if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", SSH: "box"}}); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(filepath.Join(client.ConfigDir(), "tiers.toml.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go runModelTierLoad(&Model{ctx: ctx, be: realBackend{}}, reload, done)
			t.Cleanup(func() {
				cancel()
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("model load did not end after fixture released its lock")
				}
			})
			time.Sleep(50 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("contended model load lost cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("model tier load kept waiting after cancellation")
			}
			if _, err := os.Stat(filepath.Join(client.ConfigDir(), "tiers.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled contended model created tiers.toml: %v", err)
			}
		})
	}
}
