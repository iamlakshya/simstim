package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// The save at the end of setup --host and host add compares with the
// entry that the command read at its start (docs/contributing/cli-spec.md
// 11.3, rule 13). While connecting, connectHostCore holds the host lock so
// concurrent commands fail with host_busy, and an out-of-band removal or
// recreation in hosts.toml fails the save with host_changed without
// overwriting it.

// setupHost runs setup --host box, with change run once, just before its
// save, and returns the setup's error.
func setupHost(t *testing.T, tr *clienttest.Transport, change func()) error {
	t.Helper()
	old := beforeSetupSave
	once := false
	beforeSetupSave = func(client.Host) {
		if !once {
			once = true
			change()
		}
	}
	t.Cleanup(func() { beforeSetupSave = old })
	o := guideOpts{host: "box", agents: "claude", agentsSet: true, yes: true, noLogin: true}
	g, _, _ := testGuide(t, context.Background(), o, onboard.System{Providers: providers}, tr, "", false)
	return g.run()
}

// withSaved saves box, a host that host create made, and returns it, its
// machine, and the transport of the fake, with Claude Code ready on the
// host.
func withSaved(t *testing.T) (client.Host, *clienttest.Machine, *clienttest.Transport) {
	t.Helper()
	m, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{readyCheck("claude", "Claude Code", api.LoginYes)})
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "1"}}
	if err := saveHost(h); err != nil {
		t.Fatal(err)
	}
	return h, m, tr
}

// A host that is removed while setup --host connects it stays forgotten:
// a concurrent host rm is blocked by setup's host lock, and an out-of-band
// removal of hosts.toml fails the save with host_changed.
func TestSetupHostKeepsARemovedHostRemoved(t *testing.T) {
	_, _, tr := withSaved(t)
	var rmErr error
	err := setupHost(t, tr, func() {
		rmErr = removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: "box"})
		if err := forgetHost("box"); err != nil {
			t.Error(err)
		}
	})
	var rmUE, setupUE *client.UserError
	if !errors.As(rmErr, &rmUE) || rmUE.Code != "host_busy" || rmUE.Msg != "box is busy: setup changes it" {
		t.Fatalf("concurrent host rm during setup --host: %+v", rmErr)
	}
	if !errors.As(err, &setupUE) || setupUE.Code != "host_changed" || setupUE.Msg != "box was removed while setup ran, so it was not saved" {
		t.Fatalf("setup --host of a host removed meanwhile: setup %+v", err)
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the removed host came back")
	}
}

// A host made again while setup --host connects the old one keeps its new
// entry: a concurrent host rm is blocked by setup's host lock, and an
// out-of-band replacement in hosts.toml fails the save with host_changed.
func TestSetupHostKeepsAHostMadeAgain(t *testing.T) {
	_, _, tr := withSaved(t)
	var rmErr error
	err := setupHost(t, tr, func() {
		rmErr = removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
		recreated := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "2"}}
		if err := saveHost(recreated); err != nil {
			t.Error(err)
		}
	})
	var rmUE, setupUE *client.UserError
	if !errors.As(rmErr, &rmUE) || rmUE.Code != "host_busy" || rmUE.Msg != "box is busy: setup changes it" {
		t.Fatalf("concurrent host rm during setup --host: %+v", rmErr)
	}
	if !errors.As(err, &setupUE) || setupUE.Code != "host_changed" || setupUE.Msg != "box was made again while setup ran, so it was not saved" {
		t.Fatalf("setup --host of a host made again meanwhile: setup %+v", err)
	}
	if now, err := client.FindHost("box"); err != nil || now.Machine == nil || now.Machine.Created != "2" {
		t.Errorf("the setup overwrote the new host: %+v, %v", now, err)
	}
}

// host add compares too: a host that a hand edit changed while host add
// connected it keeps the edit.
func TestHostAddKeepsAChangeMadeMeanwhile(t *testing.T) {
	h, _, _ := withSaved(t)
	edited := h
	edited.Labels = map[string]string{"edited": "yes"}
	old := beforeSetupSave
	beforeSetupSave = func(client.Host) { _ = saveHost(edited) }
	t.Cleanup(func() { beforeSetupSave = old })
	err := addHost(context.Background(), quietPrompts(context.Background()), h, true)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "box changed while host add ran, so it was not saved" {
		t.Fatalf("host add of a host changed meanwhile: %+v", err)
	}
	if now, _ := client.FindHost("box"); now.Labels["edited"] != "yes" {
		t.Errorf("host add overwrote the edit: %+v", now)
	}
}

// host add prints the steps of the host's setup cleaned: what the host
// sent never resets the terminal or writes to its clipboard.
func TestHostAddCleansTheSteps(t *testing.T) {
	h, _, tr := withSaved(t)
	evil := "\x1bc\x1b]52;c;aGk=\x07\u202e"
	tr.Steps = []api.Check{{Name: "linger" + evil, OK: false, Detail: "off" + evil + "\nnext", Fix: "loginctl" + evil}}
	out, err := said(t, func() error { return addHost(context.Background(), quietPrompts(context.Background()), h, true) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\x1b\x07\u202e") || !strings.Contains(out, "✕ lingerc]52;c;aGk=: offc]52;c;aGk= next\n") || !strings.Contains(out, "Try: loginctlc]52;c;aGk=\n") {
		t.Errorf("host add printed %q", out)
	}
}

// A forget that failed keeps its cause: a cancel stays a cancel, so a
// Ctrl+C prints the line of any signal, and a user error keeps its code
// and hint, such as host_changed or hosts_busy. When the machine is
// deleted already, the message says so too. Only a plain error becomes
// forget_failed.
func TestForgetFailedKeepsTheCause(t *testing.T) {
	h := client.Host{Name: "box"}
	if err := forgetFailed(h, context.Canceled, true); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancel: %v", err)
	}
	for _, cause := range []*client.UserError{
		{Msg: "box changed while host rm ran, so it was not forgotten", Hint: brand.Name + " host show box (then run the command again)", Code: "host_changed"},
		{Msg: "another command held hosts.toml for 10s, so nothing was saved", Hint: "End that command, then run this one again.", Code: "hosts_busy"},
	} {
		for _, deleted := range []bool{false, true} {
			var ue *client.UserError
			err := forgetFailed(h, cause, deleted)
			if !errors.As(err, &ue) || ue.Code != cause.Code || ue.Hint != cause.Hint || !strings.HasSuffix(ue.Msg, cause.Msg) ||
				strings.HasPrefix(ue.Msg, "the machine of box is deleted, but box is still saved: ") != deleted {
				t.Errorf("%s, deleted %v: %+v", cause.Code, deleted, err)
			}
		}
	}
	var ue *client.UserError
	if err := forgetFailed(h, context.DeadlineExceeded, true); !errors.As(err, &ue) || ue.Code != "forget_failed" || !strings.Contains(ue.Msg, "the machine of box is deleted, but box is still saved: context deadline exceeded") {
		t.Errorf("a deadline after deletion: %+v", err)
	}
	if err := forgetFailed(h, errors.New("disk full"), false); !errors.As(err, &ue) || ue.Code != "forget_failed" || ue.Msg != "could not forget box: disk full" {
		t.Errorf("a plain error: %+v", err)
	}
}

// A record of a removal that host rm cannot clear, once it forgot the
// host, is a warning with its Try: line: what to delete by hand.
func TestHostRmWarnsOfARecordThatStays(t *testing.T) {
	withSaved(t)
	rec := client.LoadRemoval(client.Host{Name: "box"})
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "box-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	stuck(t, files[0])
	var said bytes.Buffer
	p := quietPrompts(context.Background())
	p.out = &said
	if err := removeHost(context.Background(), p, rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said.String(), "\nTry: Delete "+files[0]+" by hand, then run the command again.\n") {
		t.Errorf("host rm said %q", said.String())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the host was not forgotten")
	}
}
