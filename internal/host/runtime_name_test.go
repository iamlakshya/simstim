package host_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Names, and renames that meet a resume or a delete
// (docs/contributing/cli-spec.md 9.2 and 9.5).

// sessionFolders lists the folders of the host's sessions.
func sessionFolders(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// New and Rename refuse a name of the form of an ID, and a name longer
// than api.SessionNameMax, with bad_name and a hint. They make and change
// nothing. A name of another form, or of the longest length, is a name.
func TestNamesThatAreRefused(t *testing.T) {
	hosttest.New(t)
	long := strings.Repeat("é", api.SessionNameMax+1)
	for _, name := range []string{"proj2345", " abcd2345 ", long} {
		_, err := host.New(host.NewArgs{Name: name, Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
		if code := codeOf(err); code != "bad_name" || !strings.Contains(hintOf(err), " ") {
			t.Errorf("new %q: %v, with the code %q", name, err, code)
		}
	}
	if left := sessionFolders(t); len(left) != 0 {
		t.Fatalf("a refused new made %v", left)
	}
	s := start(t, "a", "sleep", "60")
	for _, name := range []string{"proj2345", long} {
		if err := host.Rename(s.ID, name); codeOf(err) != "bad_name" {
			t.Errorf("rename to %q: %v", name, err)
		}
	}
	if m, err := host.LoadManifest(s.ID); err != nil || m.Name != "a" {
		t.Fatalf("after the refused renames: %+v, %v", m, err)
	}
	for _, name := range []string{"frontend", "abcd-efgh", "abcdefg", "ABCDEFGH", "abcdefgh1", "proj-2345", strings.Repeat("é", api.SessionNameMax)} {
		if err := host.Rename(s.ID, name); err != nil {
			t.Errorf("rename to %q: %v", name, err)
		}
	}
}

// hintOf is the hint of a host error, or "".
func hintOf(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Hint
	}
	return ""
}

// Every ID that the helper makes holds a digit, so a name of 8 letters,
// such as frontend, never looks like one (cli-spec 9.2).
func TestNewIDsHoldADigit(t *testing.T) {
	for range 20000 {
		if id := host.NewID(); !api.LooksLikeID(id) || !strings.ContainsAny(id, api.SessionIDDigits) {
			t.Fatalf("NewID made %q", id)
		}
	}
}

// A session that takes its name from a folder whose name looks like an ID
// gets "-s" at the end of the name, so new never makes a session whose name
// looks like an ID. A folder named like a word keeps its name.
func TestAFolderNamedLikeAnID(t *testing.T) {
	hosttest.New(t)
	// An older helper's session, whose ID has no digit: a folder of that
	// name gives a name with "-s" too.
	if err := (&host.Manifest{Schema: 1, ID: "oldoldid", Name: "old"}).Save(); err != nil {
		t.Fatal(err)
	}
	for folder, want := range map[string]string{"proj2345": "proj2345-s", "frontend": "frontend", "oldoldid": "oldoldid-s"} {
		s, err := host.New(host.NewArgs{Cwd: "~/" + folder, MakeFolder: true, Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
		if err != nil || s.Name != want || api.LooksLikeID(s.Name) {
			t.Errorf("a session in ~/%s: %+v, %v, want the name %s", folder, s, err, want)
		}
	}
}

// A rename that the holder got, and that waits for the lock, while a
// resume stops that holder (review 4, finding 1): the holder goes without
// an answer. The rename then takes the lock itself. Here no new holder
// starts, so it renames the stopped session on disk. It never fails with
// the closed connection.
func TestARenameWhoseHolderStops(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	stall := filepath.Join(paths.SessionDir(s.ID), host.RenameStall)
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := host.LockSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	renamed := make(chan error, 1)
	go func() { renamed <- host.Rename(s.ID, "b") }()
	hosttest.WaitFor(t, 10*time.Second, "the rename to reach the holder", func() bool {
		_, err := os.Stat(filepath.Join(paths.SessionDir(s.ID), host.RenameStalled))
		return err == nil
	})
	// What a resume does first, under the lock: the old holder stops.
	pid := hosttest.HolderPID(t, s.ID)
	_, _ = host.HolderRequest(s.ID, "kill", nil, 3*time.Second)
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(stall)
	unlock()
	select {
	case err := <-renamed:
		if err != nil {
			t.Fatalf("the rename: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rename never ended")
	}
	if m, err := host.LoadManifest(s.ID); err != nil || m.Name != "b" {
		t.Errorf("after the rename: %+v, %v", m, err)
	}
}

// The same with a real resume, which starts a new holder: the rename goes
// to the new holder.
func TestARenameDuringAResume(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sh", "-c", "exit 3")
	waitState(t, s.ID, host.StateErrored)
	stall := filepath.Join(paths.SessionDir(s.ID), host.RenameStall)
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	renamed := make(chan error, 1)
	go func() { renamed <- host.Rename(s.ID, "b") }()
	hosttest.WaitFor(t, 10*time.Second, "the rename to reach the holder", func() bool {
		_, err := os.Stat(filepath.Join(paths.SessionDir(s.ID), host.RenameStalled))
		return err == nil
	})
	old := hosttest.HolderPID(t, s.ID)
	if _, err := host.Resume(api.ResumeArgs{ID: s.ID}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(stall)
	select {
	case err := <-renamed:
		if err != nil {
			t.Fatalf("the rename: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rename never ended")
	}
	pid := hosttest.HolderPID(t, s.ID)
	if pid == old {
		t.Fatal("the resume kept the old holder")
	}
	// The new holder has the name in memory too: its last save keeps it.
	_, _ = host.HolderRequest(s.ID, "kill", nil, 3*time.Second)
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if m, err := host.LoadManifest(s.ID); err != nil || m.Name != "b" {
		t.Errorf("after the rename and the holder's last save: %+v, %v", m, err)
	}
}

// A start whose delivery of the secrets fails stops its holder, and waits
// for it, before it deletes the session: the holder waits for its secrets,
// and would start its agent later and save into the deleted folder.
func TestAFailedDeliveryOfSecretsStopsTheHolder(t *testing.T) {
	hosttest.New(t)
	old := *host.Deliver
	t.Cleanup(func() { *host.Deliver = old })
	var id string
	*host.Deliver = func(sid string, _ map[string]api.Secret) error {
		id = sid
		return errors.New("the test drops the secrets")
	}
	_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	if codeOf(err) != "secrets_failed" || id == "" {
		t.Fatalf("the start: %v", err)
	}
	if namedHolderRuns(t, id) {
		t.Error("the holder of the removed session still runs, and would save into its folder")
	}
	if left := sessionFolders(t); len(left) != 0 {
		t.Errorf("the failed start left %v", left)
	}
}

// A start whose holder will not stop keeps the session, and its error says
// why: a holder that went on after a delete would start its agent later,
// and save into the deleted folder.
func TestAHolderThatWillNotStopKeepsTheSession(t *testing.T) {
	hosttest.New(t)
	oldDeliver, oldSignal, oldWait := *host.Deliver, *host.SignalHolder, *host.StopWait
	t.Cleanup(func() { *host.Deliver, *host.SignalHolder, *host.StopWait = oldDeliver, oldSignal, oldWait })
	var id string
	*host.Deliver = func(sid string, _ map[string]api.Secret) error {
		id = sid
		return errors.New("the test drops the secrets")
	}
	*host.SignalHolder = func(*os.Process, os.Signal) error { return nil }
	*host.StopWait = 200 * time.Millisecond
	_, err := host.New(host.NewArgs{Name: "a", Cwd: "~/made/here", MakeFolder: true, Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	pids := holderPIDs(t, id)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_ = hosttest.Reap(pid, 5*time.Second)
		}
	})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "secrets_failed" || !strings.Contains(pe.Message, "could not be stopped, so the session "+id+" was kept") ||
		!strings.Contains(pe.Message, "may start later without its secrets") || pe.Hint == "" {
		t.Fatalf("the start: %v", err)
	}
	if left := sessionFolders(t); len(left) != 1 || left[0] != id || len(pids) != 1 {
		t.Errorf("the start whose holder runs on left %v, and holders %v", left, pids)
	}
	// The folder that the start made stays with the kept session.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "made", "here")); err != nil {
		t.Errorf("the kept session's folder went: %v", err)
	}
}

// A holder that does not end at SIGTERM ends at SIGKILL: the start that
// failed to deliver its secrets then deletes its session.
func TestAHolderThatIgnoresSIGTERMIsKilled(t *testing.T) {
	hosttest.New(t)
	oldDeliver, oldSignal, oldWait := *host.Deliver, *host.SignalHolder, *host.StopWait
	t.Cleanup(func() { *host.Deliver, *host.SignalHolder, *host.StopWait = oldDeliver, oldSignal, oldWait })
	var id string
	*host.Deliver = func(sid string, _ map[string]api.Secret) error {
		id = sid
		return errors.New("the test drops the secrets")
	}
	*host.SignalHolder = func(p *os.Process, sig os.Signal) error {
		if sig == syscall.SIGKILL {
			return p.Signal(sig)
		}
		return nil
	}
	*host.StopWait = 200 * time.Millisecond
	_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	if codeOf(err) != "secrets_failed" || strings.Contains(err.Error(), "kept") || id == "" {
		t.Fatalf("the start: %v", err)
	}
	if namedHolderRuns(t, id) || len(sessionFolders(t)) != 0 {
		t.Errorf("the holder runs %v, and the start left %v", namedHolderRuns(t, id), sessionFolders(t))
	}
}

// A holder that answers after the start's limit is not left to start its
// agent later, without its secrets: the start stops it, and deletes the
// session, as after a failed delivery. A resume stops it too, and keeps
// its session.
func TestAHolderThatAnswersLateIsStopped(t *testing.T) {
	hosttest.New(t)
	oldWait := *host.StartWait
	t.Cleanup(func() { *host.StartWait = oldWait })
	stall := filepath.Join(t.TempDir(), "start-stall")
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := start(t, "ended", "sh", "-c", "exit 3")
	waitState(t, s.ID, host.StateErrored)
	t.Setenv(host.StartStallEnv, stall)
	*host.StartWait = 500 * time.Millisecond

	_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	b, _ := os.ReadFile(stall + ".stalled")
	id := string(b)
	if codeOf(err) != "start_failed" || !strings.Contains(err.Error(), "so the session was removed") || id == "" {
		t.Fatalf("the start: %v (stalled %q)", err, id)
	}
	if namedHolderRuns(t, id) {
		t.Error("the holder that answered late runs on, and would start its agent")
	}
	if left := sessionFolders(t); len(left) != 1 || left[0] != s.ID {
		t.Errorf("the start left %v", left)
	}

	if err := os.Remove(stall + ".stalled"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Resume(api.ResumeArgs{ID: s.ID}); err == nil {
		t.Fatal("a resume whose holder answered late succeeded")
	}
	if namedHolderRuns(t, s.ID) {
		t.Error("the resumed holder that answered late runs on")
	}
	if _, err := host.LoadManifest(s.ID); err != nil {
		t.Errorf("the resume did not keep its session: %v", err)
	}
}

// holderEnded counts only an end that the holder recorded before any
// agent started (review 7, finding 1): an end after the agent started, or
// a state that is no end, is a holder that may still run its agent.
func TestWhatCountsAsAHolderThatEnded(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		state string
		agent int
		want  bool
	}{
		{host.StateErrored, 0, true},
		{host.StateExited, 0, true},
		{host.StateErrored, 4242, false},
		{host.StateExited, 4242, false},
		{host.StateWorking, 4242, false},
		{host.StateStarting, 0, false},
	} {
		m := &host.Manifest{Schema: 1, ID: fakeID, Name: "a", Status: host.Status{State: c.state}}
		m.Run.AgentPID = c.agent
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		if got := host.HolderEnded(fakeID); got != c.want {
			t.Errorf("holderEnded with %s and the agent %d = %v", c.state, c.agent, got)
		}
	}
}

// A holder that answers late, and starts its agent between the start's
// time-out and its look at the manifest, is late too: whether its agent is
// working or has already ended with a non-zero AgentPID, the start stops
// the holder and deletes the session.
func TestAHolderThatStartsItsAgentLateIsStopped(t *testing.T) {
	for _, c := range []struct {
		name  string
		argv  []string
		state string
	}{
		{"working", []string{"sleep", "60"}, host.StateWorking},
		{"ended after starting", []string{"/bin/sh", "-c", "exit 3"}, host.StateErrored},
	} {
		t.Run(c.name, func(t *testing.T) {
			hosttest.New(t)
			oldWait, oldLate := *host.StartWait, *host.LateStart
			t.Cleanup(func() { *host.StartWait, *host.LateStart = oldWait, oldLate })
			stall := filepath.Join(t.TempDir(), "start-stall")
			if err := os.WriteFile(stall, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(host.StartStallEnv, stall)
			*host.StartWait = 500 * time.Millisecond
			reached := false
			*host.LateStart = func(id string) {
				// The start gave up: the holder goes on now, and starts its agent.
				_ = os.Remove(stall)
				hosttest.WaitFor(t, 10*time.Second, "the late holder's agent to reach "+c.state, func() bool {
					m, err := host.LoadManifest(id)
					return err == nil && m.Status.State == c.state && m.Run.AgentPID != 0
				})
				reached = true
			}
			_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: c.argv, Cols: 80, Rows: 24})
			b, _ := os.ReadFile(stall + ".stalled")
			id := string(b)
			if !reached || codeOf(err) != "start_failed" || !strings.Contains(err.Error(), "so the session was removed") {
				t.Fatalf("the start: %v (reached %s first: %v)", err, c.state, reached)
			}
			if namedHolderRuns(t, id) || len(sessionFolders(t)) != 0 {
				t.Errorf("the late holder runs %v, and the start left %v", namedHolderRuns(t, id), sessionFolders(t))
			}
		})
	}
}

// New and Rename refuse a name that is the ID of a session on this host,
// as an older helper's ID without a digit: an address with that word finds
// that session first.
func TestANameThatIsTheIDOfASession(t *testing.T) {
	hosttest.New(t)
	if err := (&host.Manifest{Schema: 1, ID: "oldoldid", Name: "old"}).Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := host.New(host.NewArgs{Name: "oldoldid", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24}); codeOf(err) != "bad_name" || hintOf(err) == "" {
		t.Errorf("new with the ID of a session as its name: %v", err)
	}
	s := start(t, "a", "sleep", "60")
	if err := host.Rename(s.ID, "oldoldid"); codeOf(err) != "bad_name" {
		t.Errorf("a rename to the ID of a session: %v", err)
	}
	if err := host.Rename(s.ID, "newnewid"); err != nil {
		t.Errorf("a rename to an ID that no session has: %v", err)
	}
}

// namedHolderRuns reports whether a holder process of session id runs.
func namedHolderRuns(t *testing.T, id string) bool {
	t.Helper()
	return len(holderPIDs(t, id)) > 0
}

// holderPIDs lists the holder processes of session id.
func holderPIDs(t *testing.T, id string) []int {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ps", "-A", "-o", "pid=,args=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		pid, args, _ := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); err == nil && strings.HasSuffix(strings.TrimSpace(args), " holder "+id) {
			pids = append(pids, n)
		}
	}
	return pids
}
