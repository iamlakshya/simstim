package tui

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPaletteSessionShellKeepsExactSelectionAndTerminalContext(t *testing.T) {
	hs := home(t, 80, 24)
	m := hs.m
	h := m.hostByName("atlas-dev")
	m.openHost(h)
	m.sel = sessKey{host: h.name(), id: h.sessions[0].ID}
	_, selected := m.selectedPair()
	if selected == nil {
		t.Fatal("no selected fixture session")
	}
	id, parent := selected.ID, h.conn
	var command tea.ExecCommand
	m.exec = func(c tea.ExecCommand, _ tea.ExecCallback) tea.Cmd {
		command = c
		return nil
	}
	closed, runs := 0, 0
	var once sync.Once
	m.sessionShell = func(ctx context.Context, got Conn, host, target string) (func(*os.File, *os.File) error, func() error, error) {
		if ctx != m.ctx || got != parent || host != "atlas-dev" || target != id {
			t.Fatal("the action changed its original context, parent or exact ID")
		}
		return func(in, out *os.File) error {
			runs++
			if in == nil || out == nil {
				t.Fatal("the actual terminal files were lost")
			}
			return nil
		}, func() error { once.Do(func() { closed++ }); return nil }, nil
	}
	hs.do(m.openSessionShell())
	if command == nil {
		t.Fatal("session shell did not reserve the existing terminal handoff")
	}
	m.sel = sessKey{host: h.name(), id: h.sessions[len(h.sessions)-1].ID}
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	command.SetStdin(file)
	command.SetStdout(file)
	if err := command.Run(); err != nil || runs != 1 || closed != 1 {
		t.Fatal("the captured shell action did not run/close exactly once", err, runs, closed)
	}
}

func TestPaletteSessionShellCancelledIntentDoesNotRun(t *testing.T) {
	hs := home(t, 80, 24)
	m := hs.m
	ctx, cancel := context.WithCancel(m.ctx)
	m.ctx = ctx
	defer cancel()
	h := m.hostByName("atlas-dev")
	m.openHost(h)
	var command tea.ExecCommand
	m.exec = func(c tea.ExecCommand, _ tea.ExecCallback) tea.Cmd { command = c; return nil }
	runs, closed := 0, false
	m.sessionShell = func(context.Context, Conn, string, string) (func(*os.File, *os.File) error, func() error, error) {
		return func(*os.File, *os.File) error { runs++; return nil }, func() error { closed = true; return nil }, nil
	}
	hs.do(m.openSessionShell())
	if command == nil {
		t.Fatal("no prepared shell intent")
	}
	cancel()
	if err := command.Run(); !errors.Is(err, context.Canceled) || runs != 0 || !closed {
		t.Fatal("cancelled shell intent ran or dropped its held owner", err, runs, closed)
	}
}

func TestPaletteShellAndLockdownNameActualCLICommands(t *testing.T) {
	found := map[string]string{}
	for _, action := range Palette() {
		found[action.Name] = action.CLI
	}
	if found["session: shell"] != "shell" || found["policy: lockdown"] != "policy lockdown" ||
		found["session: policy"] != "policy ls --in" || found["session: policy log"] != "policy log --in" {
		t.Fatal("palette actions lost their actual CLI operation", found)
	}
}

func TestPaletteLockdownUsesInjectedRealCommandHandoff(t *testing.T) {
	hs := home(t, 80, 24)
	m := hs.m
	var command tea.ExecCommand
	m.exec = func(c tea.ExecCommand, _ tea.ExecCallback) tea.Cmd { command = c; return nil }
	called := false
	m.policyLockdown = func(ctx context.Context, in, out *os.File) error {
		called = ctx == m.ctx && in != nil && out != nil
		return nil
	}
	hs.do(m.runPolicyLockdown())
	if command == nil || called {
		t.Fatal("lockdown bypassed its ordinary terminal reservation")
	}
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	command.SetStdin(file)
	command.SetStdout(file)
	if err := command.Run(); err != nil || !called {
		t.Fatal("lockdown lost its actual terminal/context handoff", err)
	}
}
