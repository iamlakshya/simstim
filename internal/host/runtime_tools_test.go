package host_test

import (
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestKeyTestInASession(t *testing.T) {
	h := hosttest.New(t)
	helper := h.Install("test", "")
	s := start(t, "keys", helper, "keytest")
	waitOutput(t, s.ID, "Press keys")
	if !strings.Contains(peek(t, s.ID), "\x1b[>31u") {
		t.Error("the key tester did not turn on the kitty keyboard protocol")
	}
	for _, k := range []string{"\x1b[13;2u", "x", "q", "q"} {
		if _, err := host.HolderRequest(s.ID, "send", map[string]any{"text": k}, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitState(t, s.ID, host.StateExited)
	out := peek(t, s.ID)
	for _, want := range []string{`"\x1b[13;2u"`, `"x"`, `"q"`, "\x1b[<u"} {
		if !strings.Contains(out, want) {
			t.Errorf("the key tester did not print %q: %q", want, out)
		}
	}
}

func TestPhonePath(t *testing.T) {
	hosttest.New(t)
	term := hosttest.NewTerminal(t, 40, 20) // a phone held upright
	t.Cleanup(func() { signal.Reset(syscall.SIGQUIT) })
	done := make(chan error, 1)
	run := func() { go func() { done <- host.RunPick(term.TTY, term.TTY) }() }
	run()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	term.WaitFor("No sessions are running on ")

	s := start(t, "phone", "/bin/sh")
	run()
	term.WaitFor("Pick 1-1, or q to quit: ")
	if out := string(term.Output()); !strings.Contains(out, " 1  phone") || strings.Contains(out, "custom") {
		t.Errorf("menu = %q: a narrow menu has no agent column", out)
	}
	term.Type("7\n")
	term.WaitFor("pick a number from 1 to 1")
	term.Type("phone\n")
	term.WaitFor("Attaching to phone. Ctrl+\\ detaches.")
	term.Type("echo PHONE-$((4*4))\r")
	term.WaitFor("PHONE-16")
	term.Type("\x1c")
	term.WaitFor("Detached. phone keeps running.")
	term.Type("1\n")
	term.WaitFor("Attaching to phone.")
	term.Type("exit 6\r")
	term.WaitFor("phone ended (exit code 6).")
	term.Type("q\n")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the menu did not quit")
	}
	// The holder saves its manifest once a second, and once more when the
	// program ends, just after it tells the clients: wait for that save.
	hosttest.WaitFor(t, 5*time.Second, "the attach in the manifest", func() bool {
		m, err := host.LoadManifest(s.ID)
		return err == nil && m.LastAttachedAt.After(time.Time{})
	})
}

func TestManifestKeepsFieldsItDoesNotKnow(t *testing.T) {
	hosttest.New(t)
	s := start(t, "future", "sleep", "60")
	crash(t, s.ID)
	b, err := os.ReadFile(paths.Manifest(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	all["from_a_newer_helper"] = json.RawMessage(`{"x":1}`)
	b, _ = json.Marshal(all)
	if err := os.WriteFile(paths.Manifest(s.ID), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := host.Rename(s.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(paths.Manifest(s.ID))
	var kept map[string]json.RawMessage
	var renamed string
	if err != nil || json.Unmarshal(b, &kept) != nil || string(kept["from_a_newer_helper"]) != `{"x":1}` || json.Unmarshal(kept["name"], &renamed) != nil || renamed != "renamed" {
		t.Errorf("manifest = %s", b)
	}
	if _, err := host.LoadManifest("nosuchid"); err == nil {
		t.Error("a missing manifest loaded")
	}
	if err := os.WriteFile(paths.Manifest(s.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.LoadManifest(s.ID); err == nil {
		t.Error("a broken manifest loaded")
	}
	if list, _ := host.List(); len(list) != 0 {
		t.Errorf("a session with a broken manifest is listed: %+v", list)
	}
}

func TestLoginShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh-of-the-test")
	if got := host.LoginShell(); got != "/bin/zsh-of-the-test" {
		t.Errorf("LoginShell = %q", got)
	}
	t.Setenv("SHELL", "")
	if got := host.LoginShell(); !strings.HasPrefix(got, "/") {
		t.Errorf("LoginShell without $SHELL = %q, want a path from the password file, or /bin/sh", got)
	}
}
