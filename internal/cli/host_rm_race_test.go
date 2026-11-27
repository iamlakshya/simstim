package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// host rm and another command at once (docs/contributing/cli-spec.md
// 11.3): the machine that host rm deletes is the one that it asked about,
// and no host create of that name runs between its last look and the
// forget.

// A host made again while the question waits has a new machine: Lima and
// Docker give it the same ID, but not the same stamp. host rm deletes
// nothing, and forgets nothing.
func TestHostRmWhenTheHostIsMadeAgain(t *testing.T) {
	fm, h := withCreated(t)
	again := h
	again.Machine = &client.Machine{Provider: "fake", ID: "fake-box", Created: "2026-11-12T08:00:00Z"}
	old := afterAnswer
	afterAnswer = func(string) {
		if err := saveHost(again); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterAnswer = old })
	var out bytes.Buffer
	err := removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_changed" || ue.Msg != "box was made again while host rm looked at it, so it changed nothing" ||
		exitCode(ue.Kind) != 1 {
		t.Fatalf("err = %+v", err)
	}
	if fm.destroyed() {
		t.Error("host rm deleted the machine that was made again")
	}
	if now, err := client.FindHost("box"); err != nil || now.Machine.Created != again.Machine.Created {
		t.Errorf("the new host: %+v, %v", now, err)
	}

	// A change of any other field counts too, as does a host removed.
	afterAnswer = func(string) {
		_ = saveHost(client.Host{Name: "box", Machine: again.Machine, Labels: map[string]string{"k": "v"}})
	}
	err = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if !errors.As(err, &ue) || ue.Msg != "box changed while host rm looked at it, so it changed nothing" {
		t.Errorf("a changed host: %+v", err)
	}
	afterAnswer = func(string) { _ = forgetHost("box") }
	err = removeHost(context.Background(), testPrompts("box\n", &out), rmOpts{name: "box"})
	if !errors.As(err, &ue) || ue.Msg != "box was removed while host rm looked at it" {
		t.Errorf("a removed host: %+v", err)
	}
	if fm.destroyed() {
		t.Error("host rm deleted a machine")
	}
}

// lockOf takes the lock of host name for the command kind, as another
// command would, until the test lets it go.
func lockOf(t *testing.T, name, kind string) func() {
	t.Helper()
	_, unlock, err := client.LockHost(context.Background(), name, kind)
	if err != nil {
		t.Fatal(err)
	}
	return unlock
}

// While another command holds the lock of the host, host rm, host create
// and host set change nothing, and say which command holds it.
func TestTheLockOfAHost(t *testing.T) {
	fm, _ := withCreated(t)
	unlock := lockOf(t, "box", "host set")
	err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_busy" || ue.Msg != "box is busy: host set changes it" || fm.destroyed() {
		t.Fatalf("host rm while the host is locked: %+v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("host rm forgot a locked host")
	}
	if err := setHost(context.Background(), io.Discard, "box", []string{"idle=2h"}); !errors.As(err, &ue) || ue.Code != "host_busy" {
		t.Fatalf("host set while the host is locked: %+v", err)
	}
	p, _ := findProvider("fake")
	held := lockOf(t, "new", "host rm")
	var made bytes.Buffer
	req := quietRequest("new")
	req.Out = &made
	if err := createHost(context.Background(), p, req); !errors.As(err, &ue) || ue.Msg != "new is busy: host rm changes it" || made.Len() != 0 {
		t.Fatalf("host create while the host is locked: %+v, %q", err, made.String())
	}
	held()
	unlock()
	if err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false)); err != nil || !fm.destroyed() {
		t.Fatalf("host rm after the unlock: %v", err)
	}
}

// host create holds the lock through the last save of its setup: a host
// rm at that moment fails with host_busy, and the host and its machine
// stay as the create made them.
func TestCreateHoldsTheLockToItsLastSave(t *testing.T) {
	m, _ := withFake(t, nil)
	p, _ := findProvider("fake")
	var rmErr error
	saves := 0
	old := beforeSetupSave
	beforeSetupSave = func(h client.Host) {
		saves++
		rmErr = removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts(h.Name, true))
	}
	t.Cleanup(func() { beforeSetupSave = old })
	if err := createHost(context.Background(), p, quietRequest("box")); err != nil {
		t.Fatal(err)
	}
	var ue *client.UserError
	if saves != 1 || !errors.As(rmErr, &ue) || ue.Code != "host_busy" || ue.Msg != "box is busy: host create changes it" {
		t.Fatalf("host rm during the last save of host create: %d saves, %+v", saves, rmErr)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Error("the host is gone")
	}
	for _, c := range m.Calls() {
		if c == "destroy" {
			t.Error("the machine was deleted during the create")
		}
	}
}

// The guided setup's create holds the lock through the save of its
// connect too, and lets it go after.
func TestSetupHoldsTheLockToItsLastSave(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.SetAgents([]api.AgentCheck{readyCheck("claude", "Claude Code", api.LoginYes)})
	var rmErr error
	old := beforeSetupSave
	beforeSetupSave = func(h client.Host) {
		rmErr = removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts(h.Name, true))
	}
	t.Cleanup(func() { beforeSetupSave = old })
	o := guideOpts{create: "fake", name: "box", agents: "claude", agentsSet: true, yes: true, noLogin: true}
	g, out, _ := testGuide(t, context.Background(), o, onboard.System{Providers: providers}, tr, "", false)
	if err := g.run(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var ue *client.UserError
	if !errors.As(rmErr, &ue) || ue.Msg != "box is busy: setup changes it" {
		t.Fatalf("host rm during the setup's last save: %+v", rmErr)
	}
	if g.unlockHost != nil {
		t.Error("the setup still holds the lock")
	}
	_, unlock, err := client.LockHost(context.Background(), "box", "host rm")
	if err != nil {
		t.Fatalf("the lock after the setup: %v", err)
	}
	// The setup's context no longer says that it holds the lock.
	if _, _, err := client.LockHost(g.ctx, "box", "setup"); !errors.As(err, &ue) || ue.Code != "host_busy" {
		t.Errorf("a lock with the setup's context while host rm holds it: %+v", err)
	}
	unlock()
}

// Every look of host rm happens under the lock, --destroy --confirm's
// too: work that starts just before the lock is taken stops the delete.
func TestHostRmLooksUnderTheLock(t *testing.T) {
	fm, _ := withCreated(t, client.Session{ID: "abcd2345", Name: "quiet", State: api.StateIdle, Alive: true})
	old := beforeHostLock
	beforeHostLock = func(string) {
		fm.tr.SetSessions([]client.Session{{ID: "abcd2345", Name: "quiet", State: api.StateWorking, Alive: true}})
	}
	t.Cleanup(func() { beforeHostLock = old })
	err := removeHost(context.Background(), quietPrompts(context.Background()), destroyOpts("box", false))
	if err == nil || !strings.Contains(err.Error(), "1 session on box is working: quiet") || fm.destroyed() {
		t.Fatalf("host rm --destroy --confirm box: %v, destroyed %v", err, fm.destroyed())
	}
}

// host rm writes no record while another command holds the lock: its first
// look comes after the lock, so a busy host keeps its record as it was.
func TestABusyHostKeepsItsRecord(t *testing.T) {
	fm, h := withCreated(t)
	fm.SetState(client.MachineMissing)
	rec := client.LoadRemoval(h)
	if err := rec.Record("purge"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "box-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	before, _ := os.ReadFile(files[0])
	unlock := lockOf(t, "box", "host create")
	defer unlock()
	for _, o := range []rmOpts{destroyOpts("box", false), {name: "box"}} {
		var ue *client.UserError
		if err := removeHost(context.Background(), testPrompts("\n", &bytes.Buffer{}), o); !errors.As(err, &ue) || ue.Code != "host_busy" {
			t.Fatalf("host rm of a busy host: %+v", err)
		}
	}
	if after, _ := os.ReadFile(files[0]); string(after) != string(before) {
		t.Errorf("the record changed from %s to %s", before, after)
	}
}

// host create clears the record of an earlier removal before it makes
// anything. When the record cannot be cleared, it makes nothing. A
// folder that is not empty, where the record should be, cannot be
// removed on any system.
func TestCreateWithARecordThatStays(t *testing.T) {
	withFake(t, nil)
	rec := client.LoadRemoval(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "1"}})
	if err := rec.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "box-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	stuck(t, files[0])
	p, _ := findProvider("fake")
	var made bytes.Buffer
	req := quietRequest("box")
	req.Out = &made
	err := createHost(context.Background(), p, req)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "removal_record" || !strings.Contains(ue.Msg, "so nothing was made") || made.Len() != 0 {
		t.Fatalf("host create with a record that stays: %+v, made %q", err, made.String())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("a host was saved")
	}
}

// stuck puts a folder that is not empty at path, which os.Remove then
// cannot remove, on every system, until the test ends.
func stuck(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}
