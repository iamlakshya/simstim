package host_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSessionLifecycle(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "first", "/bin/sh", "-c", "echo READY-$((40+2)); exec sleep 60")
	if s.ID == "" || s.Name != "first" || s.Agent != "custom" || !s.Alive || (s.State != host.StateStarting && s.State != host.StateWorking) {
		t.Fatalf("new returned %+v", s)
	}
	if s.Cwd != h.Home {
		t.Errorf("cwd = %q, want the home folder %q", s.Cwd, h.Home)
	}
	waitOutput(t, s.ID, "READY-42")

	list, err := host.List()
	if err != nil || len(list) != 1 || list[0].ID != s.ID || !list[0].Alive {
		t.Fatalf("list = %+v, %v", list, err)
	}
	m := live(t, s.ID)
	if m.Run.HolderPID == 0 || m.Run.AgentPID == 0 || m.Run.Socket != paths.Socket(s.ID) || m.HolderBinary == "" {
		t.Errorf("the holder did not record itself: %+v", m.Run)
	}
	if fi, err := os.Stat(paths.Socket(s.ID)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v, %v", fi, err)
	}

	res, err := host.Delete(s.ID)
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if err := hosttest.Reap(m.Run.HolderPID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("the session's folder is still there: %v", err)
	}
	if list, _ := host.List(); len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestListIsNewestFirstAndShowsEndedSessions(t *testing.T) {
	hosttest.New(t)
	if list, err := host.List(); err != nil || len(list) != 0 {
		t.Fatalf("list with no sessions = %+v, %v", list, err)
	}
	// It ends a moment after New returns: New saves the manifest once more
	// after the holder has started, and an end before that save is lost.
	a := start(t, "a", "/bin/sh", "-c", "sleep 0.5; exit 3")
	b := start(t, "b", "sleep", "60")
	waitState(t, a.ID, host.StateErrored)
	list, err := host.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("order = %s, %s; want the newest first", list[0].Name, list[1].Name)
	}
	ended := list[1]
	if ended.State != host.StateErrored || ended.ExitCode == nil || *ended.ExitCode != 3 {
		t.Errorf("an agent that exited with 3: %+v", ended)
	}
	if !ended.Alive {
		t.Errorf("the holder of an ended agent stays, so the output can be read: %+v", ended)
	}
}

func TestNewChecksItsArguments(t *testing.T) {
	h := hosttest.New(t)
	writeFile(t, filepath.Join(h.Home, "a-file"), "x\n")
	for _, c := range []struct {
		args host.NewArgs
		code string
		msg  string // a part of the message, if it matters
	}{
		{host.NewArgs{Agent: "no-such-agent"}, "unknown_agent", ""},
		{host.NewArgs{Argv: []string{"true"}, Cwd: "~/missing"}, api.CodeNoFolder, ""},
		{host.NewArgs{Argv: []string{"true"}, Cwd: "~/missing", Worktree: true, MakeFolder: true}, "bad_folder", "needs a git repository"},
		// A missing agent comes first: the user is not asked about a folder
		// for a start that cannot run.
		{host.NewArgs{Argv: []string{"no-such-program-here"}, Cwd: "~/missing"}, "agent_missing", ""},
		{host.NewArgs{Argv: []string{"no-such-program-here"}, Cwd: "~/missing", MakeFolder: true}, "agent_missing", ""},
		// Only a full path is made: not ~user, and not a path relative to
		// the folder that the helper runs in.
		{host.NewArgs{Argv: []string{"true"}, Cwd: "~nobody-here/missing", MakeFolder: true}, "bad_folder", "does not exist"},
		{host.NewArgs{Argv: []string{"true"}, Cwd: "missing-here/sub", MakeFolder: true}, "bad_folder", "does not exist"},
		// A file above the folder is in the way, whatever the permissions.
		{host.NewArgs{Argv: []string{"true"}, Cwd: "~/a-file/sub", MakeFolder: true}, "bad_folder", filepath.Join(h.Home, "a-file") + " on this host is a file"},
		{host.NewArgs{Argv: []string{"no-such-program-here"}}, "agent_missing", ""},
		{host.NewArgs{Argv: []string{"true"}, Cwd: "~", Worktree: true}, "not_a_repo", ""},
	} {
		_, err := host.New(c.args)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != c.code || pe.Hint == "" || !strings.Contains(pe.Message, c.msg) {
			t.Errorf("new %+v = %v, want a %s error with a hint", c.args, err, c.code)
		}
	}
	for _, dir := range []string{filepath.Join(h.Home, "missing"), "~nobody-here", "missing-here"} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("a start that failed made %s: %v", dir, err)
		}
	}
	// A program that cannot run: the holder reports why, and New says so.
	// With a secret, the holder answers on its socket and takes the secret
	// before it starts the program, so New learns of the failure only from
	// the start itself.
	bad := filepath.Join(h.Home, "not-a-program")
	writeFile(t, bad, "this is not a script\n")
	if err := os.Chmod(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, secrets := range []map[string]api.Secret{nil, {"TEST_TOKEN": "fake-value"}} {
		_, err := host.New(host.NewArgs{Argv: []string{bad}, Secrets: secrets})
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "start_failed" || !strings.Contains(pe.Message, "exec format error") {
			t.Fatalf("new with a program that cannot run, and %d secrets = %v", len(secrets), err)
		}
	}
	var list []host.SessionInfo
	hosttest.WaitFor(t, 5*time.Second, "the holders of the sessions that failed to start to exit", func() bool {
		list, _ = host.List()
		return len(list) == 2 && !list[0].Alive && !list[1].Alive
	})
	for _, s := range list {
		if s.State != host.StateErrored {
			t.Errorf("the session that failed to start: %+v", s)
		}
	}
}

// New makes a missing folder, and the folders above it, with the mode that
// mkdir -p gives them. A start that fails after that removes them again,
// here because the repository's setup script needs an answer first.
func TestNewMakesTheFolderAndRemovesItAfterAFailedStart(t *testing.T) {
	h := hosttest.New(t)
	dir := repo(t, h)
	hook := filepath.Join(dir, "."+brand.Name, "setup")
	writeFile(t, hook, "#!/bin/sh\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	a := host.NewArgs{Argv: []string{"sleep", "60"}, Cwd: "~/src/app/pkg/new", MakeFolder: true}
	_, err := host.New(a)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeSetupTrust {
		t.Fatalf("new = %v, want a %s question", err, api.CodeSetupTrust)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg")); !os.IsNotExist(err) {
		t.Fatalf("the start that failed left its folders behind: %v", err)
	}
	defer syscall.Umask(syscall.Umask(0o022))
	a.Setup = api.SetupSkip
	s, err := host.New(a)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cwd != filepath.Join(dir, "pkg", "new") {
		t.Errorf("the session's folder is %s", s.Cwd)
	}
	for _, d := range []string{filepath.Join(dir, "pkg"), s.Cwd} {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Errorf("%s has mode %o, want 755", d, st.Mode().Perm())
		}
	}
}

func TestShellIsTheDefaultAgent(t *testing.T) {
	hosttest.New(t)
	s, err := host.New(host.NewArgs{Cwd: "~", Term: "no-such-terminal", TermProgram: "TestTerm", TermProgramVersion: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != "shell" {
		t.Errorf("agent = %q, want shell", s.Agent)
	}
	m := manifest(t, s.ID)
	if len(m.Agent.Argv) != 2 || m.Agent.Argv[0] != "/bin/sh" || m.Agent.Argv[1] != "-l" {
		t.Errorf("argv = %q, want the login shell", m.Agent.Argv)
	}
	if m.Term.Term != "xterm-256color" {
		t.Errorf("an unknown TERM falls back to xterm-256color, got %q", m.Term.Term)
	}
	env := strings.Join(m.Env, "\n")
	for _, want := range []string{"TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=TestTerm", brand.Env("SESSION") + "=" + s.ID, brand.Env("SOCK") + "=" + paths.Socket(s.ID)} {
		if !strings.Contains(env, want) {
			t.Errorf("the agent's environment lacks %s", want)
		}
	}
	send(t, s.ID, "echo SHELL-$((6*7)) $"+brand.Env("SESSION"))
	waitOutput(t, s.ID, "SHELL-42 "+s.ID)
}

func TestHolderRequests(t *testing.T) {
	hosttest.New(t)
	s := start(t, "req", "/bin/sh")
	send(t, s.ID, "echo ONE")
	waitOutput(t, s.ID, "ONE")

	raw, err := host.HolderRequest(s.ID, "info", nil, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var m host.Manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.ID != s.ID {
		t.Fatalf("info = %s, %v", raw, err)
	}

	raw, err = host.HolderRequest(s.ID, "send", map[string]any{"text": "echo TWO"}, 3*time.Second)
	if err != nil || string(raw) != `{"bytes":8}` {
		t.Fatalf("send without enter = %s, %v", raw, err)
	}
	send(t, s.ID, "")
	waitOutput(t, s.ID, "TWO")

	for _, c := range []struct {
		op   string
		args any
		code string
	}{
		{"rename", map[string]string{"name": "  "}, "bad_request"},
		{"rename", "not an object", "bad_request"},
		{"send", "not an object", "bad_request"},
		{"no-such-op", nil, "unknown_op"},
	} {
		_, err := host.HolderRequest(s.ID, c.op, c.args, 3*time.Second)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != c.code {
			t.Errorf("%s %v = %v, want %s", c.op, c.args, err, c.code)
		}
	}
	if _, err := host.HolderRequest("nosuchid", "info", nil, time.Second); err == nil {
		t.Error("a request to a session that does not exist worked")
	}
}

func TestRename(t *testing.T) {
	hosttest.New(t)
	s := start(t, "old", "sleep", "60")
	if err := host.Rename(s.ID, "  new name  "); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "the new name", func() bool { return live(t, s.ID).Name == "new name" })
	if err := host.Rename(s.ID, " "); err == nil {
		t.Error("an empty name was taken")
	}
	// With its holder gone, the manifest is changed directly.
	pid := hosttest.HolderPID(t, s.ID)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := host.Rename(s.ID, "offline"); err != nil {
		t.Fatal(err)
	}
	if m := manifest(t, s.ID); m.Name != "offline" {
		t.Errorf("name = %q", m.Name)
	}
	list, _ := host.List()
	if len(list) != 1 || list[0].State != host.StateLost || list[0].Alive || list[0].Reason == "" {
		t.Errorf("a session whose holder died: %+v", list)
	}
	var pe *proto.Error
	if err := host.Rename("nosuchid", "x"); !errors.As(err, &pe) || pe.Code != "not_found" {
		t.Errorf("rename of a missing session = %v", err)
	}
}

func TestDeleteRefusesWhatIsNotASession(t *testing.T) {
	hosttest.New(t)
	for _, id := range []string{"", "../etc", "UPPER", "nosuchid"} {
		_, err := host.Delete(id)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "not_found" {
			t.Errorf("delete %q = %v", id, err)
		}
	}
}

func TestStopWithTheHost(t *testing.T) {
	hosttest.New(t)
	s := start(t, "sys", "sleep", "60")
	pid := hosttest.HolderPID(t, s.ID)
	// SIGTERM is how the system stops a session, for example at shutdown.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	m := manifest(t, s.ID)
	if m.Status.State == host.StateExited || m.Status.State == host.StateErrored || m.Status.Source != "system" {
		t.Fatalf("a session stopped with the host must stay restorable: %+v", m.Status)
	}
	if _, err := os.Stat(paths.Socket(s.ID)); !os.IsNotExist(err) {
		t.Errorf("the socket is still there: %v", err)
	}
}

// A holder that is slow to exit must not write its manifest again after
// Delete removed the session's folder.
func TestDeletedSessionStaysDeleted(t *testing.T) {
	h := hosttest.New(t)
	// Under the race detector, each holder pauses for a second as it exits:
	// long enough for its once-a-second save to run again. Without the race
	// detector the window is short, but it is there.
	t.Setenv("GORACE", "log_path="+filepath.Join(h.Root, "race"))
	s := start(t, "busy", "/bin/sh", "-c", "while :; do echo tick; sleep 0.1; done")
	waitOutput(t, s.ID, "tick")
	time.Sleep(1100 * time.Millisecond) // a save while the session works
	pid := hosttest.HolderPID(t, s.ID)
	if _, err := host.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); !os.IsNotExist(err) {
		b, _ := os.ReadFile(paths.Manifest(s.ID))
		t.Fatalf("the deleted session came back: %v\n%s", err, b)
	}
}
