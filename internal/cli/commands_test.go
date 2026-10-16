package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The tests in this binary may start it again as a session's holder.
func TestMain(m *testing.M) { hosttest.Main(m) }

// testHost is a host called "box" in the client's config, served by the
// real host code on this machine. Its SSH target runs commands here too,
// through a fake ssh, for the commands that use ssh itself.
type testHost struct {
	h  *hosttest.Host
	ch client.Host
	tr *hosttest.Transport
}

func newTestHost(t *testing.T) *testHost {
	t.Helper()
	h := hosttest.New(t)
	ch, tr := h.ClientHost("box")
	ch.SSH = "box"
	h.Script("ssh", `echo "$@" >> "$HOME/ssh.log"
for a; do last=$a; done
case " $* " in *" -N "*|*" -O "*) exit 0;; esac
exec sh -c "$last"
`)
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	if err := client.SaveHosts([]client.Host{ch}); err != nil {
		t.Fatal(err)
	}
	return &testHost{h: h, ch: ch, tr: tr}
}

// run runs the command line and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	root := Root()
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	os.Stdout = old
	_ = w.Close()
	return <-out, err
}

// ok runs the command line, fails the test on an error, and returns the output.
func ok(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// fails runs the command line, which must fail with a user error whose
// message holds want.
func fails(t *testing.T, want string, args ...string) {
	t.Helper()
	failsWithHint(t, want, "", args...)
}

// failsWithHint is fails, with a hint that must hold hint.
func failsWithHint(t *testing.T, want, hint string, args ...string) {
	t.Helper()
	out, err := run(t, args...)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, want) || !strings.Contains(ue.Hint, hint) {
		t.Fatalf("%s = %v (output %q), want an error about %q with a hint about %q", strings.Join(args, " "), err, out, want, hint)
	}
}

var startedRE = regexp.MustCompile(`Started box/(\w+) \(`)

// newSession starts a detached session and returns its ID.
func newSession(t *testing.T, name string, argv ...string) string {
	t.Helper()
	out := ok(t, append([]string{"new", "box", "--detach", "--name", name, "--"}, argv...)...)
	m := startedRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("new printed %q", out)
	}
	return m[1]
}

func peekOutput(t *testing.T, id string) string {
	t.Helper()
	raw, err := host.HolderRequest(id, "peek", nil, 3*time.Second)
	if err != nil {
		return ""
	}
	var p struct{ Data []byte }
	_ = json.Unmarshal(raw, &p)
	return string(p.Data)
}

func TestSessionCommands(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "web", "/bin/sh")
	if out := ok(t, "ls"); !regexp.MustCompile(`box\s+` + id + `\s+web\s+shell\s+(working|idle)`).MatchString(out) {
		t.Errorf("ls = %q", out)
	}
	var listed []hostSessions
	if err := json.Unmarshal([]byte(ok(t, "ls", "--json", "--host", "box")), &listed); err != nil || len(listed) != 1 || len(listed[0].Sessions) != 1 || listed[0].Sessions[0].ID != id {
		t.Errorf("ls --json = %+v, %v", listed, err)
	}
	if out := ok(t, "send", "box/web", "echo SENT-$((3*3))"); out != "Sent to web on box.\n" {
		t.Errorf("send = %q", out)
	}
	hosttest.WaitFor(t, 5*time.Second, "the sent command's output", func() bool { return strings.Contains(peekOutput(t, id), "SENT-9") })
	ok(t, "send", "--no-enter", "box/"+id, "echo NOT-YET")
	if out := ok(t, "rename", "box/"+id, "api"); out != "Renamed web to api on box.\n" {
		t.Errorf("rename = %q", out)
	}
	hosttest.WaitFor(t, 5*time.Second, "the new name", func() bool { return strings.Contains(ok(t, "ls"), " api ") })
	if out := ok(t, "ports", "box/api"); out != "api on box listens on no TCP ports.\n" {
		t.Errorf("ports = %q", out)
	}
	if out := ok(t, "kill", "box/api"); out != "Deleted api ("+id+") on box.\n" {
		t.Errorf("kill = %q", out)
	}
	if out := ok(t, "ls"); out != "box  no sessions\n" {
		t.Errorf("ls after kill = %q", out)
	}
}

func TestSessionsAreFoundByIDOrUniqueName(t *testing.T) {
	newTestHost(t)
	newSession(t, "dup", "sleep", "60")
	newSession(t, "dup", "sleep", "60")
	failsWithHint(t, "2 sessions are named", "session ID", "kill", "box/dup")
	fails(t, `no session "nope" on box`, "kill", "box/nope")
	fails(t, "is not host/session", "kill", "box")
	fails(t, `no host named "elsewhere"`, "kill", "elsewhere/x")
	fails(t, `no host named "elsewhere"`, "ls", "--host", "elsewhere")
	fails(t, `no host named "elsewhere"`, "new", "elsewhere")
}

func TestAttachNeedsATerminal(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "s", "sleep", "60")
	fails(t, "attach needs a terminal", "attach", "box/"+id)
	fails(t, "attach needs a terminal", "new", "box", "--", "sleep", "60")
	fails(t, "no session has been attached yet", "attach", "-")
	// A session whose holder is gone cannot be attached to: a resume brings
	// it back first.
	lost := newSession(t, "lost", "sleep", "60")
	pid := hosttest.HolderPID(t, lost)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	failsWithHint(t, "the session lost on box ended (the session's holder stopped)", "resume box/"+lost, "attach", "box/lost")
}

func TestVersionAndConfig(t *testing.T) {
	newTestHost(t)
	if out := ok(t, "version"); !strings.Contains(out, "(protocol ") {
		t.Errorf("version = %q", out)
	}
	out := ok(t, "config", "path")
	if !strings.HasSuffix(out, "config.toml\n"+filepath.Join(client.ConfigDir(), "hosts.toml")+"\n") {
		t.Errorf("config path = %q", out)
	}
	if out := ok(t, "config", "keymap"); !strings.Contains(out, "session: delete") {
		t.Errorf("config keymap = %q", out)
	}
	var km []map[string]any
	if err := json.Unmarshal([]byte(ok(t, "config", "keymap", "--json")), &km); err != nil || len(km) == 0 {
		t.Errorf("config keymap --json: %v", err)
	}
}
