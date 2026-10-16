package cli

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ls says why each ended agent ended. The host marks every non-zero exit
// "errored", so the cause comes from the out-of-memory mark, the signal and
// the exit code.
func TestStateLabelSaysWhyTheAgentEnded(t *testing.T) {
	code := func(n int) *int { return &n }
	cases := []struct {
		s    client.Session
		want string
	}{
		{client.Session{State: api.StateErrored, Reason: "oom", OOM: true, Signal: "killed", ExitCode: code(137)}, "errored (out of memory)"},
		{client.Session{State: api.StateErrored, OOM: true, Signal: "SIGKILL"}, "errored (out of memory)"},
		// The cap stopped another process before the agent exited. An older
		// holder sets the mark again after the exit, and a newer one gives
		// the reason. Without SIGKILL, the mark is not how the agent ended.
		{client.Session{State: api.StateExited, ExitCode: code(0), OOM: true, Reason: api.ReasonChildOOM}, "exited (a process hit the cap)"},
		{client.Session{State: api.StateErrored, ExitCode: code(1), OOM: true}, "errored (exit code 1, a process hit the cap)"},
		{client.Session{State: api.StateErrored, ExitCode: code(1), Reason: api.ReasonChildOOM}, "errored (exit code 1, a process hit the cap)"},
		{client.Session{State: api.StateErrored, Signal: "killed", ExitCode: code(137)}, "errored (killed)"},
		{client.Session{State: api.StateErrored, ExitCode: code(3)}, "errored (exit code 3)"},
		{client.Session{State: api.StateExited, ExitCode: code(0)}, "exited"},
		{client.Session{State: api.StateLost, Reason: "the host restarted"}, "lost (the host restarted)"},
		// The memory cap stopped another process: the agent runs on.
		{client.Session{State: api.StateWorking, OOM: true, Alive: true}, "working"},
		{client.Session{State: api.StateNeedsInput, Reason: "Run the tests?", Alive: true}, "needs input"},
	}
	for _, c := range cases {
		if got := stateLabel(c.s); got != c.want {
			t.Errorf("stateLabel(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

// A long state keeps the columns after it in line.
func TestPrintSessionsKeepsColumns(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	printSessions([]hostSessions{{Host: "box", Sessions: []client.Session{
		{ID: "a1", Name: "app", Agent: "claude", Cwd: "/srv/app", State: api.StateErrored, OOM: true, Signal: "killed", CreatedAt: time.Now()},
		{ID: "b2", Name: "api", Agent: "codex", Cwd: "/srv/api", State: api.StateWorking, CreatedAt: time.Now()},
	}}})
	os.Stdout = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "errored (out of memory)") {
		t.Fatalf("ls printed %q", lines)
	}
	if a, b := strings.Index(lines[0], "/srv/app"), strings.Index(lines[1], "/srv/api"); a != b {
		t.Fatalf("the folders do not line up:\n%s\n%s", lines[0], lines[1])
	}
}

// Through a real host: an agent that exits with a code, and one that a
// signal kills. ls says why each ended, and attach says what to try, while
// their holders still answer.
func TestLsSaysWhyAnAgentEnded(t *testing.T) {
	newTestHost(t)
	crash := newSession(t, "crash", "/bin/sh", "-c", "exit 3")
	shot := newSession(t, "shot", "/bin/sh", "-c", "kill -KILL $$")
	want := []*regexp.Regexp{
		regexp.MustCompile(crash + `\s+crash\s+.*errored \(exit code 3\)`),
		regexp.MustCompile(shot + `\s+shot\s+.*errored \(killed\)`),
	}
	hosttest.WaitFor(t, 10*time.Second, "ls to say why both agents ended", func() bool {
		out := ok(t, "ls")
		return want[0].MatchString(out) && want[1].MatchString(out)
	})
	failsWithHint(t, "the session crash on box ended (exit code 3)", "resume box/"+crash, "attach", "box/crash")
}

// attach to an agent that has ended reads like the message at the end of an
// attach, with the same step after the memory cap: a resume, which can give
// it a bigger cap. After a clean exit, the step also offers to remove it,
// and for a lost agent, the restore of every lost session.
func TestEndedErrorSaysWhatToTry(t *testing.T) {
	h := client.Host{Name: "box"}
	capped := client.Session{ID: "a1", Name: "app", State: api.StateErrored, OOM: true, Signal: "killed", ExitCode: new(137)}
	var ue *client.UserError
	if err := endedError(h, capped); !errors.As(err, &ue) || ue.Msg != "the session app on box ended (out of memory)" ||
		ue.Hint != client.EndHint(&proto.Ended{OOM: true}, "box/a1") || !strings.Contains(ue.Hint, "resume box/a1") ||
		!strings.Contains(ue.Hint, "--memory-max") {
		t.Fatalf("endedError = %+v", err)
	}
	if got := endMessage("app", "box", "a1", &proto.Ended{OOM: true, Signal: "killed", ExitCode: 137}); !strings.Contains(got, "ended (out of memory)") ||
		!strings.Contains(got, "Try: "+ue.Hint) {
		t.Fatalf("endMessage = %q", got)
	}
	done := client.Session{ID: "b2", Name: "docs", State: api.StateExited, ExitCode: new(0)}
	if err := endedError(h, done); !errors.As(err, &ue) || ue.Msg != "the session docs on box ended (exit code 0)" ||
		!strings.Contains(ue.Hint, "resume box/b2") || !strings.Contains(ue.Hint, "kill box/b2") {
		t.Fatalf("endedError = %+v", err)
	}
	lost := client.Session{ID: "c3", Name: "pair", State: api.StateLost, Reason: "the host restarted"}
	if err := endedError(h, lost); !errors.As(err, &ue) || ue.Msg != "the session pair on box ended (the host restarted)" ||
		!strings.Contains(ue.Hint, " resume box/c3   (") || !strings.Contains(ue.Hint, "host restore box --all") {
		t.Fatalf("endedError = %+v", err)
	}
}
