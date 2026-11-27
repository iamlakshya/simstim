//go:build unix

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func launcherShellParent(t *testing.T, rows []client.Session) (client.Host, *client.Conn, func() int) {
	t.Helper()
	h, _, transport := withSaved(t)
	transport.Sessions = rows
	id := "0123456789abcdef0123456789abcdef"
	if err := client.SetTierContext(t.Context(), h.Name, client.TierOwn, id, ""); err != nil {
		t.Fatal(err)
	}
	parent, err := client.Dial(t.Context(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(parent.Close)
	return h, parent, func() int { return len(transport.Remotes()) }
}

func TestLauncherShellRejectsWrongHostAndCancelledContext(t *testing.T) {
	h, parent, starts := launcherShellParent(t, nil)
	before := starts()
	if run, closeScope, err := runLauncherSessionShell(t.Context(), parent, "other", "a2a2a2a2"); err == nil || run != nil || closeScope != nil {
		t.Fatal("wrong host produced a shell intent")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if run, closeScope, err := runLauncherSessionShell(ctx, parent, h.Name, "a2a2a2a2"); !errors.Is(err, context.Canceled) || run != nil || closeScope != nil {
		t.Fatal("cancelled action produced a shell intent", err)
	}
	if starts() != before {
		t.Fatal("invalid shell action started transport")
	}
}

func TestLauncherShellNeverResolvesLegacySelectedIDAsName(t *testing.T) {
	const selected = "abcdefgh"
	h, parent, _ := launcherShellParent(t, []client.Session{{ID: "z9z9z9z9", Name: selected, Agent: "shell", Alive: true}})
	run, closeScope, err := runLauncherSessionShell(t.Context(), parent, h.Name, selected)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	terminal := hosttest.NewTerminal(t, 80, 24)
	err = run(terminal.TTY, terminal.TTY)
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "session_not_found" || !strings.Contains(user.Msg, selected) {
		t.Fatal("a missing exact legacy ID fell through to another session's name", err)
	}
}

func TestLauncherShellRejectsIdenticalReaddAfterPreparation(t *testing.T) {
	h, parent, starts := launcherShellParent(t, nil)
	run, closeScope, err := runLauncherSessionShell(t.Context(), parent, h.Name, "a2a2a2a2")
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	before := starts()
	if err := client.SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveHosts(t.Context(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
	terminal := hosttest.NewTerminal(t, 80, 24)
	if err := run(terminal.TTY, terminal.TTY); err == nil || starts() != before {
		t.Fatal("prepared action refreshed its original Source after identical re-add", err)
	}
}
