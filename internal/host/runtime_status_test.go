package host_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// waitStatus waits until a session's status has the state and source.
func waitStatus(t *testing.T, id, state, source string) *host.Manifest {
	t.Helper()
	var m *host.Manifest
	hosttest.WaitFor(t, 10*time.Second, "state "+state+" from "+source, func() bool {
		var err error
		m, err = host.LoadManifest(id)
		return err == nil && m.Status.State == state && m.Status.Source == source
	})
	return m
}

func TestStatusFromOutput(t *testing.T) {
	hosttest.New(t)
	s := start(t, "status", "/bin/sh")
	// A bell means the program wants the user.
	send(t, s.ID, "printf '\\a'")
	waitStatus(t, s.ID, host.StateNeedsInput, "notify")
	// A progress report (OSC 9;4) keeps it working while it is quiet.
	send(t, s.ID, "printf '\\033]9;4;3\\033\\\\'")
	waitStatus(t, s.ID, host.StateWorking, "progress")
	// When the progress ends, three quiet seconds after the last output, it
	// is idle.
	send(t, s.ID, "printf '\\033]9;4;0\\033\\\\'")
	m := waitStatus(t, s.ID, host.StateIdle, "quiet")
	if m.LastActivityAt.IsZero() {
		t.Error("the last activity was not recorded")
	}
	// The full screen is noted too: here no agent's screen is read.
	if m.Status.FullScreen {
		t.Error("a custom command's screen is not read")
	}
}

// fakeClaude installs a fake claude in ~/bin, which the user's login profile
// puts on PATH, as many people's do. It records its arguments, then runs
// body. The helper is installed too, so that claude's hooks can run.
func fakeClaude(t *testing.T, h *hosttest.Host, body string) {
	t.Helper()
	h.Install("test", "")
	writeFile(t, filepath.Join(h.Home, ".profile"), "PATH=\"$HOME/bin:$PATH\"; export PATH\n")
	bin := filepath.Join(h.Home, "bin", "claude")
	writeFile(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/claude.args\"\n"+body+"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
}

// hookCommand returns the command line claude would run for an event, from
// the settings it was given.
func hookCommand(t *testing.T, h *hosttest.Host, event string) string {
	t.Helper()
	var args []string
	hosttest.WaitFor(t, 5*time.Second, "claude's arguments", func() bool {
		b, err := os.ReadFile(filepath.Join(h.Home, "claude.args"))
		args = strings.Split(strings.TrimSpace(string(b)), "\n")
		return err == nil && len(args) >= 4
	})
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if args[2] != "--settings" || json.Unmarshal([]byte(args[3]), &settings) != nil {
		t.Fatalf("claude was started with %q", args)
	}
	cmd := settings.Hooks[event][0].Hooks[0].Command
	if want := filepath.Join(paths.BinDir(), "current", brand.HostBinary); !strings.Contains(cmd, want) {
		t.Fatalf("hook command %q does not run the current helper %s", cmd, want)
	}
	return cmd
}

func TestStatusFromHooks(t *testing.T) {
	h := hosttest.New(t)
	fakeClaude(t, h, "exec /bin/sh")
	events := filepath.Join(h.Home, "events")
	if _, err := host.AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{
		Events:       []string{"needs_input", "failed", "started"},
		Delay:        "0s",
		Detail:       ptr(true),
		Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + events + "; echo >> " + events}},
	}}); err != nil {
		t.Fatal(err)
	}
	s, err := host.New(host.NewArgs{Agent: "claude", Name: "hooked", Cwd: "~"})
	if err != nil {
		t.Fatal(err)
	}
	// An ID that is not a UUID could be a flag for claude's resume command.
	// The holder does not keep it, and its log says why. The hooks run in
	// turn, so the second ID is the only one in the history.
	const id = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	send(t, s.ID, "echo '{\"session_id\":\"--dangerously-skip-permissions\",\"source\":\"startup\"}' | "+hookCommand(t, h, "SessionStart"))
	send(t, s.ID, "echo '{\"session_id\":\""+id+"\",\"source\":\"clear\"}' | "+hookCommand(t, h, "SessionStart"))
	hosttest.WaitFor(t, 10*time.Second, "the agent's own session ID", func() bool {
		a := live(t, s.ID).Agent
		return a.AgentSessionID == id && a.IDSource == "hook" && len(a.IDHistory) == 1
	})
	log, err := os.ReadFile(paths.HolderLog(s.ID))
	if err != nil || !strings.Contains(string(log), `msg="session ID from a hook not kept"`) ||
		!strings.Contains(string(log), "id=--dangerously-skip-permissions") {
		t.Errorf("the holder's log does not say why it kept no ID (%v):\n%s", err, log)
	}
	send(t, s.ID, "echo '{\"message\":\"Claude needs your permission\"}' | "+hookCommand(t, h, "Notification"))
	m := waitStatus(t, s.ID, host.StateNeedsInput, "notify")
	if m.Status.Reason != "Claude needs your permission" {
		t.Errorf("reason = %q", m.Status.Reason)
	}
	send(t, s.ID, hookCommand(t, h, "UserPromptSubmit")+" </dev/null")
	waitStatus(t, s.ID, host.StateWorking, "hook")
	send(t, s.ID, hookCommand(t, h, "Stop")+" </dev/null")
	waitStatus(t, s.ID, host.StateIdle, "hook")

	// The alerts went to the command: started, then the question.
	hosttest.WaitFor(t, 10*time.Second, "the needs_input alert", func() bool {
		b, _ := os.ReadFile(events)
		return strings.Contains(string(b), "Claude needs your permission")
	})
	b, _ := os.ReadFile(events)
	if !strings.Contains(string(b), `"started"`) || !strings.Contains(string(b), `"hooked"`) {
		t.Errorf("events = %s", b)
	}
}

func TestHookOutsideASessionDoesNothing(t *testing.T) {
	hosttest.New(t)
	t.Setenv(brand.Env("HOOK_SOCK"), "")
	t.Setenv(brand.Env("SOCK"), "")
	host.RunHook("stop", nil) // returns at once
	t.Setenv(brand.Env("HOOK_SOCK"), filepath.Join(t.TempDir(), "none.sock"))
	host.RunHook("stop", []string{`{"type":"agent-turn-complete"}`}) // nothing listens
}

// A session that an older helper started names only its session socket,
// and its holder is that older helper too, which takes hooks there. A plain
// listener stands in for it: the hook sends its event to the session socket.
func TestTheHookFallsBackToAnOlderHoldersSessionSocket(t *testing.T) {
	h := hosttest.New(t)
	sock := filepath.Join(h.Root, "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan proto.Request, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r, w := bufio.NewReader(c), proto.NewWriter(c)
		f, err := proto.Read(r)
		var hello proto.Hello
		if err != nil || json.Unmarshal(f.Payload, &hello) != nil || hello.Role != "hook" {
			return
		}
		_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: "1.0", Role: "holder"})
		if f, err = proto.Read(r); err != nil {
			return
		}
		var req proto.Request
		_ = json.Unmarshal(f.Payload, &req)
		got <- req
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true})
	}()
	t.Setenv(brand.Env("HOOK_SOCK"), "")
	t.Setenv(brand.Env("SOCK"), sock)
	host.RunHook("notification", []string{`{"message":"old"}`})
	select {
	case req := <-got:
		if req.Op != "hook" || !strings.Contains(string(req.Args), `"old"`) {
			t.Errorf("the older holder got %+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook sent nothing to the session socket")
	}
}

// A resume or a restore gives a session that an older helper started the
// hook socket, in place of the session socket.
func TestAResumeGivesAnOldSessionTheHookSocket(t *testing.T) {
	hosttest.New(t)
	s := start(t, "old", "/bin/sh", "-c", `echo "HOOK=$`+brand.Env("HOOK_SOCK")+` SOCK=$`+brand.Env("SOCK")+`."; sleep 0.3`)
	waitState(t, s.ID, host.StateExited)
	m := manifest(t, s.ID)
	var env []string
	for _, kv := range m.Env {
		if !strings.HasPrefix(kv, brand.Env("HOOK_SOCK")+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, brand.Env("SOCK")+"="+paths.Socket(s.ID))
	m.Env = env
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	resume(t, api.ResumeArgs{ID: s.ID})
	waitOutput(t, s.ID, "HOOK="+paths.HookSocket(s.ID)+" SOCK=.")
	got := strings.Join(manifest(t, s.ID).Env, "\n")
	if !strings.Contains(got, brand.Env("HOOK_SOCK")+"="+paths.HookSocket(s.ID)) || strings.Contains(got, brand.Env("SOCK")+"=") {
		t.Errorf("the environment after the resume:\n%s", got)
	}
}

func TestCodexNotifyHook(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "codex-like", "sleep", "60")
	t.Setenv(brand.Env("HOOK_SOCK"), paths.HookSocket(s.ID))
	t.Setenv(brand.Env("SOCK"), filepath.Join(h.Root, "wrong.sock"))
	host.RunHook("notification", []string{`{"message":"waiting"}`})
	if m := waitStatus(t, s.ID, host.StateNeedsInput, "notify"); m.Status.Reason != "waiting" {
		t.Errorf("reason = %q", m.Status.Reason)
	}
	host.RunHook("prompt", []string{"not json"})
	waitStatus(t, s.ID, host.StateWorking, "hook")
	// Codex passes its event as the last argument.
	host.RunHook("codex-notify", []string{`{"type":"agent-turn-complete","thread-id":"t1"}`})
	waitStatus(t, s.ID, host.StateIdle, "hook")
}

func TestFailedSessionAlerts(t *testing.T) {
	h := hosttest.New(t)
	events := filepath.Join(h.Home, "events")
	if _, err := host.AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{
		Events:       []string{"failed", "exited"},
		Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + events + "; echo >> " + events}},
	}}); err != nil {
		t.Fatal(err)
	}
	start(t, "fails", "/bin/sh", "-c", "sleep 0.2; exit 4")
	start(t, "ends", "/bin/sh", "-c", "sleep 0.2; exit 0")
	hosttest.WaitFor(t, 10*time.Second, "two alerts", func() bool {
		b, _ := os.ReadFile(events)
		return strings.Count(string(b), "\n\n") >= 1 && strings.Contains(string(b), `"failed"`) && strings.Contains(string(b), `"exited"`)
	})
	b, _ := os.ReadFile(events)
	if !strings.Contains(string(b), `"exit_code":4`) {
		t.Errorf("events = %s", b)
	}
}

func ptr[T any](v T) *T { return &v }
