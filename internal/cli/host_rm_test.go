package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/provider"
)

// fakeMachine is the fake machine of a created host, and its transport.
type fakeMachine struct {
	*clienttest.Machine
	tr *clienttest.Transport
}

// withCreated saves box, a host that `host create` made, on a fake
// machine with sessions. withFake gives it its own HOME and state folder,
// so the records of removals are the test's own.
func withCreated(t *testing.T, sessions ...client.Session) (*fakeMachine, client.Host) {
	t.Helper()
	m, tr := withFake(t, sessions)
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "2026-11-11T10:00:00Z"}}
	if err := saveHost(h); err != nil {
		t.Fatal(err)
	}
	return &fakeMachine{m, tr}, h
}

// destroyed reports whether the fake machine got a delete.
func (fm *fakeMachine) destroyed() bool { return slices.Contains(fm.Calls(), "destroy") }

// The second acceptance line of C7: at a terminal, host rm on a created
// machine says that it holds 2 sessions, and asks for the typed name. The
// name deletes the machine, then forgets the host.
func TestHostRmAsksForTheTypedName(t *testing.T) {
	fm, _ := withCreated(t, client.Session{ID: "abcd2345", Name: "one", State: api.StateIdle, Alive: true},
		client.Session{ID: "bcde2345", Name: "two", State: api.StateExited})
	var out bytes.Buffer
	if err := removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	want := "box was made by `host create`: the fake machine fake-box. It holds 2 sessions.\n" +
		"Type box to delete the machine too, or press Enter to keep it: " +
		"Deleted the fake machine fake-box, with its disk and every session on it.\nForgot box.\n"
	if out.String() != want {
		t.Errorf("host rm said %q, want %q", out.String(), want)
	}
	if !fm.destroyed() {
		t.Error("the machine was not destroyed")
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the host is still saved")
	}
}

// Any other text than the name cancels, and nothing changes.
func TestHostRmAWrongNameCancels(t *testing.T) {
	fm, _ := withCreated(t)
	var out bytes.Buffer
	err := removeHost(context.Background(), testPrompts("bxo\n", &out), rmOpts{name: "box"})
	if !isCancel(err) || exitCode(describe(err, nil).kind) != 130 {
		t.Fatalf("err = %v", err)
	}
	if fm.destroyed() {
		t.Error("the machine was destroyed")
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Error("the host was forgotten")
	}
}

// A unit test of cli-spec 11.6: --destroy refuses when the machine runs
// and its session list fails, or when the provider cannot tell its state,
// as when a session may be working. --force goes on.
func TestHostRmDestroyRefusesWhenTheListFails(t *testing.T) {
	fm, _ := withCreated(t)
	fm.tr.Fail("ls", &proto.Error{Code: "bad_request", Message: "the list broke"})
	err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindFailed || !strings.Contains(ue.Msg, "runs, but its sessions could not be listed, so one may be working") ||
		ue.Hint != brand.Name+" host rm box --destroy --force --confirm box (stops them too)" {
		t.Fatalf("err = %+v", err)
	}
	if fm.destroyed() {
		t.Fatal("destroyed while the list failed")
	}
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", true)); err != nil {
		t.Fatal(err)
	}
	if !fm.destroyed() {
		t.Error("--force did not destroy it")
	}
}

// A machine whose state its provider cannot tell counts as one where a
// session works: --destroy refuses before the question, and the typed name
// refuses after it.
func TestHostRmRefusesWhenTheStateIsNotKnown(t *testing.T) {
	fm, _ := withCreated(t)
	fm.StatusErr = errors.New("the provider broke")
	var out bytes.Buffer
	err := removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box", destroy: true})
	if err == nil || !strings.Contains(err.Error(), "the state of the fake machine fake-box is not known, so a session may be working on it") ||
		strings.Contains(out.String(), "Type box") {
		t.Fatalf("rm --destroy: %v\n%s", err, out.String())
	}
	out.Reset()
	err = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if err == nil || !strings.Contains(err.Error(), "is not known") || !strings.Contains(out.String(), "Its state is not known: the provider broke.") {
		t.Fatalf("rm: %v\n%s", err, out.String())
	}
	if fm.destroyed() {
		t.Error("destroyed while the state was not known")
	}
}

// A working session: with --destroy, rm refuses before it asks. Without
// it, rm asks, and refuses after the typed name. The host stays saved.
func TestHostRmRefusesWhileASessionWorks(t *testing.T) {
	fm, _ := withCreated(t, client.Session{ID: "bcde2345", Name: "busy", State: api.StateWorking, Alive: true})
	var out bytes.Buffer
	err := removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box", destroy: true})
	if err == nil || !strings.Contains(err.Error(), "1 session on box is working: busy") || strings.Contains(out.String(), "Type box") {
		t.Fatalf("rm --destroy: %v\n%s", err, out.String())
	}
	out.Reset()
	err = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if err == nil || !strings.Contains(out.String(), "It holds 1 session. 1 is working: busy.") || !strings.Contains(out.String(), "Type box") {
		t.Fatalf("rm: %v\n%s", err, out.String())
	}
	if fm.destroyed() {
		t.Error("destroyed while a session works")
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Error("the host was forgotten")
	}
}

// Work that starts while the question waits stops the delete: host rm
// looks at the machine again after the typed name.
func TestHostRmLooksAgainAfterTheQuestion(t *testing.T) {
	fm, _ := withCreated(t, client.Session{ID: "abcd2345", Name: "quiet", State: api.StateIdle, Alive: true})
	old := afterAnswer
	afterAnswer = func(string) {
		fm.tr.SetSessions([]client.Session{{ID: "abcd2345", Name: "quiet", State: api.StateWorking, Alive: true}})
	}
	t.Cleanup(func() { afterAnswer = old })
	var out bytes.Buffer
	err := removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if err == nil || !strings.Contains(err.Error(), "1 session on box is working: quiet") || !strings.Contains(out.String(), "It holds 1 session.\n") {
		t.Fatalf("rm: %v\n%s", err, out.String())
	}
	if fm.destroyed() {
		t.Error("destroyed although work started while the question waited")
	}
	// A list that fails after the question refuses too.
	afterAnswer = func(string) { fm.tr.Fail("ls", &proto.Error{Code: "bad_request", Message: "the list broke"}) }
	fm.tr.SetSessions(nil)
	err = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if err == nil || !strings.Contains(err.Error(), "could not be listed") || fm.destroyed() {
		t.Fatalf("rm with a list that fails after the question: %v", err)
	}
}

// Host text never reaches the terminal as it is: the names of the
// sessions in the question are cleaned.
func TestHostRmCleansTheSessionNames(t *testing.T) {
	withCreated(t, client.Session{ID: "abcd2345", Name: "evil\x1bc\x1b]52;c;aGk=\x07\u202ename", State: api.StateWorking, Alive: true})
	var out bytes.Buffer
	_ = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if s := out.String(); strings.ContainsAny(s, "\x1b\x07\u202e") || !strings.Contains(s, "1 is working: evilc]52;c;aGk=name.") {
		t.Errorf("the question showed %q", s)
	}
}

// A failed destroy stops the command, with exit code 1, and keeps the
// host. A record of an earlier run never skips a delete: only the
// provider says that a machine is gone (cli-spec 11.3, rule 10).
func TestHostRmRecordsEachPart(t *testing.T) {
	fm, h := withCreated(t)
	fm.DestroyErr = errors.New("the disk is busy")
	err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
	if err == nil || exitCode(describe(err, nil).kind) != 1 {
		t.Fatalf("a failed destroy: %v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the host was forgotten after a failed destroy")
	}
	if client.LoadRemoval(h).Has(client.RemovalDestroy) {
		t.Fatal("a failed destroy was recorded")
	}

	// A record that says the machine is deleted, of this very machine:
	// the provider still has it, so rm deletes it.
	rec := client.LoadRemoval(h)
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	fm.DestroyErr = nil
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false)); err != nil {
		t.Fatal(err)
	}
	if !fm.destroyed() {
		t.Fatal("a record skipped the delete of a machine that the provider still has")
	}
	if client.LoadRemoval(h).Has(client.RemovalDestroy) {
		t.Error("the forget did not clear the record")
	}

	// The provider has no such machine: it counts as deleted. With the
	// record of an earlier run, and input off without --destroy, rm says
	// that the earlier run deleted it.
	if err := saveHost(h); err != nil {
		t.Fatal(err)
	}
	rec = client.LoadRemoval(h)
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	before := len(fm.Calls())
	var out bytes.Buffer
	p := quietPrompts(context.Background())
	p.out = &out
	if err := removeHost(context.Background(), p, rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fm.Calls()[before:], "destroy") || out.String() != "The fake machine fake-box was deleted by an earlier run.\nForgot box.\n" {
		t.Errorf("a missing machine: calls %v, said %q", fm.Calls()[before:], out.String())
	}
}

// A host made again with the same name gets a machine of the same ID from
// Lima and Docker. The record of a removal of the earlier machine does not
// count for it, and host create clears it: host rm --destroy deletes the
// new machine.
func TestHostRmAfterTheHostIsMadeAgain(t *testing.T) {
	m, _ := withFake(t, nil)
	p, _ := findProvider("fake")
	old := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "2026-01-01T00:00:00Z"}}
	stale := client.LoadRemoval(old)
	if err := stale.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	if err := createHost(context.Background(), p, quietRequest("box")); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("box")
	if err != nil || h.Machine.Created == "" || h.Machine.ID != old.Machine.ID {
		t.Fatalf("the new host: %+v, %v", h, err)
	}
	if client.LoadRemoval(old).Has(client.RemovalDestroy) {
		t.Error("host create did not clear the record")
	}
	// Also a record written after the create does not count for the new
	// machine, whose stamp differs.
	if err := stale.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	if client.LoadRemoval(h).Has(client.RemovalDestroy) {
		t.Error("the record of the earlier machine counts for the new one")
	}
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Calls(), "destroy") {
		t.Error("host rm --destroy did not delete the new machine")
	}
}

// The removal tests use their own state folder: a record outside it stays.
func TestHostRmLeavesOtherRecords(t *testing.T) {
	outer := t.TempDir()
	t.Setenv("XDG_STATE_HOME", outer)
	other := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "2026-11-11T10:00:00Z"}}
	rec := client.LoadRemoval(other)
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	withCreated(t)
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", outer)
	if !client.LoadRemoval(other).Has(client.RemovalDestroy) {
		t.Error("a test removed a record outside its own state folder")
	}
}

// A forget that fails keeps the host saved, so the command runs again.
// After a delete, the error says that the machine is deleted.
func TestHostRmWhenTheForgetFails(t *testing.T) {
	fm, h := withCreated(t)
	ssh := undeletableWrapper(t, "box")
	err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "the machine of box is deleted, but box is still saved") ||
		ue.Hint != brand.Name+" host rm box (after you fix the cause)" || !fm.destroyed() {
		t.Fatalf("err = %+v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the host was forgotten although its SSH config stayed")
	}
	if !client.LoadRemoval(h).Has(client.RemovalDestroy) {
		t.Fatal("the delete was not recorded")
	}
	ssh.fix()
	var out bytes.Buffer
	p := quietPrompts(context.Background())
	p.out = &out
	if err := removeHost(context.Background(), p, rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "The fake machine fake-box was deleted by an earlier run.\nForgot box.\n" {
		t.Errorf("the second run said %q", out.String())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the second run did not forget the host")
	}
}

// The usage errors of host rm, and the unknown host, whose Try: lines run
// as written.
func TestHostRmUsageErrors(t *testing.T) {
	withCreated(t)
	if err := saveHost(client.Host{Name: "mine", SSH: "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"host", "rm", "mine", "--destroy"}, 2, "mine was not made by host create, so there is no machine to delete"},
		{[]string{"host", "rm", "box", "--destroy", "--confirm", "mine"}, 2, `--confirm got "mine", which is not the name of the host box`},
		{[]string{"host", "rm", "box", "--confirm", "box"}, 2, "--confirm needs --destroy.\n\nTry: " + brand.Name + " host rm box --destroy --confirm box"},
		{[]string{"host", "rm", "box", "--force"}, 2, "--force needs --destroy.\n\nTry: " + brand.Name + " host rm box --destroy --force"},
		{[]string{"host", "rm", "BOX", "--destroy", "--confirm", "BOX"}, 3, "Did you mean \"box\"?\n\nTry: " + brand.Name + " host rm box --destroy --confirm box\n"},
		{[]string{"host", "rm", "bxx"}, 3, "Did you mean \"box\"?\n\nTry: " + brand.Name + " host rm box\n"},
		{[]string{"host", "rm", "nowhere"}, 3, "no host named \"nowhere\".\n\nTry: " + brand.Name + " host ls"},
	} {
		got, code := lineErr(t, c.args...)
		if code != c.code || !strings.Contains(got, c.msg) {
			t.Errorf("%s: exit %d\n%s", strings.Join(c.args, " "), code, got)
		}
	}
	// The plain host is forgotten, and its sessions stay.
	if said := okSaid(t, "host", "rm", "mine"); said != "Forgot mine. Its sessions and the helper stay on the host.\n" {
		t.Errorf("host rm mine said %q", said)
	}
}

// quietRequest is the request of host create for a host named name.
func quietRequest(name string) provider.Request {
	return provider.Request{Name: name, Out: io.Discard}
}

// wrapperLock keeps the generated SSH config of a host from being deleted,
// until fix.
type wrapperLock struct {
	t    *testing.T
	path string
}

// undeletableWrapper puts a folder that is not empty where the generated
// SSH config of host name goes, so that its removal fails on every
// system.
func undeletableWrapper(t *testing.T, name string) wrapperLock {
	t.Helper()
	path := wrapperOf(name)
	stuck(t, path)
	return wrapperLock{t: t, path: path}
}

// fix lets the config be deleted again: it is gone.
func (w wrapperLock) fix() {
	if err := os.RemoveAll(w.path); err != nil {
		w.t.Error(err)
	}
}
