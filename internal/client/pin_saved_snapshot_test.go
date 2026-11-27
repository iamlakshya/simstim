package client

import (
	"context"
	"errors"
	"testing"
)

// A published pin alone cannot revive a host that the client forgot. A
// staged pin belongs to an add operation and remains usable before its save.
func TestPublishedOrphanPinNeverConnects(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := writePin(h.Name, currentStage(h.Name), []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, fn := range map[string]func() error{
		"command": func() error { _, err := (&SSH{Host: h}).Command(ctx, "true"); return err },
		"verbose": func() error { _, err := (&SSH{Host: h}).VerboseLog(ctx); return err },
		"git":     func() error { _, err := GitSSHCommand(ctx, h); return err },
		"dial":    func() error { _, err := Dial(WithoutInput(ctx), h, false); return err },
	} {
		var user *UserError
		if err := fn(); !errors.As(err, &user) || user.Code != "host_key_unpinned" {
			t.Errorf("orphan pin %s: %v", name, err)
		}
	}
	if calls := sshCalls(t, dir); len(calls) != 0 {
		t.Fatalf("orphan pin ran ssh: %q", calls)
	}
}

func TestUnsavedStagedPinRemainsUsable(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := StagePin(context.Background(), h.Name); err != nil {
		t.Fatal(err)
	}
	defer AbortPin(h.Name)
	if err := writePin(h.Name, currentStage(h.Name), []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SSH{Host: h}).Command(context.Background(), "true"); err != nil {
		t.Fatalf("staged pin was refused before save: %v", err)
	}
}
