package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// A cancel while the client reads Ghostty's terminfo, before it asks the
// host to apply its fixes, sends no request: the host applies nothing.
func TestNoFixRequestAfterACancelDuringInfocmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake infocmp is a shell script")
	}
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	clienttest.WriteScript(t, filepath.Join(bin, "infocmp"), "#!/bin/sh\n: > '"+started+"'\nexec sleep 30\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tr := &clienttest.Transport{Features: []string{"doctor.fix"}}
	h := register(t, clienttest.NewMachine(client.MachineRunning, tr))
	c, err := client.Dial(context.Background(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.HostFixes(ctx, true, []string{"linger"})
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			select {
			case err := <-done:
				t.Fatalf("infocmp never started, and HostFixes ended with %v", err)
			default:
				t.Fatal("infocmp never started")
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("HostFixes after a cancel = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HostFixes did not end at the cancel")
	}
	// The bridge answers in order: once it has answered this, it has
	// seen every request before it.
	if _, err := c.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ops := tr.Requests(); slices.Contains(ops, "doctor.fix") {
		t.Errorf("a fix request reached the host after the cancel: %q", ops)
	}
}
