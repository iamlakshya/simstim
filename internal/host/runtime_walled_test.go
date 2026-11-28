package host_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func startWalled(t *testing.T, name string, argv ...string) *host.SessionInfo {
	t.Helper()
	s, err := host.New(host.NewArgs{Name: name, Cwd: "~", Argv: argv, Cols: 80, Rows: 24, Wall: "plain"})
	if err != nil {
		t.Fatalf("new walled %s: %v", name, err)
	}
	return s
}

func TestWalledPlainLifecycleAndSingleListener(t *testing.T) {
	hosttest.New(t)
	s := startWalled(t, "plain-life", "/bin/sh", "-c", "echo WALLED-42; exec sleep 60")
	if s.Wall == nil || s.Wall.Driver != "plain" || s.Wall.Level != "none" {
		t.Fatalf("wall = %+v, want plain/none", s.Wall)
	}
	if !s.Alive {
		t.Fatalf("expected alive session: %+v", s)
	}
	waitOutput(t, s.ID, "WALLED-42")

	// Session socket exists (0600); host hook socket must NOT exist.
	if fi, err := os.Stat(paths.Socket(s.ID)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("session socket %s: %v, %v", paths.Socket(s.ID), fi, err)
	}
	if _, err := os.Stat(paths.HookSocket(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("walled session must not open host hook socket %s: %v", paths.HookSocket(s.ID), err)
	}

	m := live(t, s.ID)
	if m.Run.HolderPID == 0 || m.Run.AgentPID == 0 {
		t.Fatalf("supervisor did not record PIDs: %+v", m.Run)
	}
	if runtime.GOOS == "linux" {
		assertSupervisorListensOnlyOnSessionSocket(t, m.Run.HolderPID, paths.Socket(s.ID))
	}

	// Rename works through the supervisor.
	if err := host.Rename(s.ID, "plain-renamed"); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "renamed walled session", func() bool {
		list, err := host.List()
		return err == nil && len(list) == 1 && list[0].Name == "plain-renamed" && list[0].Wall != nil && list[0].Wall.Driver == "plain"
	})

	audit, err := os.ReadFile(paths.AuditLog(s.ID))
	if err != nil || !strings.Contains(string(audit), `"start"`) || !strings.Contains(string(audit), `"rename"`) {
		t.Fatalf("audit log = %q, %v", audit, err)
	}

	// Delete stops supervisor + guest init + guest holder and cleans up.
	res, err := host.Delete(s.ID)
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if err := hosttest.Reap(m.Run.HolderPID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("session dir still exists: %v", err)
	}
	if _, err := os.Stat(paths.GuestRuntimeDir(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("guest runtime dir still exists: %v", err)
	}
}

func assertSupervisorListensOnlyOnSessionSocket(t *testing.T, pid int, wantSock string) {
	t.Helper()
	// Collect socket inodes held by the supervisor process.
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		t.Fatalf("read supervisor fds: %v", err)
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if rest, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(rest, "]")] = true
		}
	}
	// Verify zero TCP/UDP sockets belong to the supervisor.
	for _, netFile := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		b, err := os.ReadFile(netFile)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) >= 10 && inodes[fields[9]] {
				t.Errorf("supervisor holds a network socket in %s: %s", netFile, line)
			}
		}
	}
	// Verify the only listening Unix socket owned by the supervisor is paths.Socket(id).
	b, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		t.Fatal(err)
	}
	var listeners []string
	for _, line := range strings.Split(string(b), "\n")[1:] {
		fields := strings.Fields(line)
		// Num RefCount Protocol Flags Type St Inode [Path]
		// Flags 00010000 (__SO_ACCEPTCON) and St 01 (SS_UNCONNECTED) marks a listening Unix socket.
		if len(fields) < 7 || !inodes[fields[6]] {
			continue
		}
		if fields[3] == "00010000" {
			path := ""
			if len(fields) >= 8 {
				path = fields[7]
			}
			listeners = append(listeners, path)
		}
	}
	if len(listeners) != 1 || listeners[0] != wantSock {
		t.Fatalf("supervisor listening Unix sockets = %q, want only [%q]", listeners, wantSock)
	}
}

func TestWalledPlainFixedBaseEnvNoLoginEnvLeak(t *testing.T) {
	h := hosttest.New(t)
	t.Setenv("HOST_LEAK_VAR", "must-not-enter-sandbox")
	writeFile(t, filepath.Join(h.Home, ".profile"), "export PROFILE_LEAK_VAR=from-login-shell\n")

	s := startWalled(t, "envcheck", "/bin/sh", "-c",
		`echo "USER=$USER LOGNAME=$LOGNAME LANG=$LANG HOME=$HOME LEAK=$HOST_LEAK_VAR:$PROFILE_LEAK_VAR HOOK=$`+brand.Env("HOOK_SOCK")+`"; exec sleep 60`)
	waitOutput(t, s.ID, "USER=agent LOGNAME=agent LANG=C.UTF-8")
	out := peek(t, s.ID)
	if !strings.Contains(out, "LEAK=:") {
		t.Fatalf("host login env leaked into walled session:\n%s", out)
	}
	if strings.Contains(out, "HOME="+h.Home+" ") {
		t.Fatalf("host HOME leaked into walled session:\n%s", out)
	}
	if strings.Contains(out, paths.Socket(s.ID)) {
		t.Fatalf("session socket leaked into walled session:\n%s", out)
	}
}

func TestWalledPlainAttachReplayResizeSnapshotAndEnd(t *testing.T) {
	h := hosttest.New(t)
	vt := host.ShadowVT()
	if vt != "" {
		defer host.SetExecutable(h.Install("test", vt))()
	}

	s := startWalled(t, "attach", "/bin/sh", "-c",
		"stty raw -echo; printf 'FIRST\\n'; head -c 1 >/dev/null; printf 'SECOND\\033[?2004h\\n'; head -c 1 >/dev/null; stty size; exit 7")
	waitOutput(t, s.ID, "FIRST")

	b := newRawBridge(t)
	a := b.attach(s.ID, -1, 100, 30)
	if vt != "" {
		if a.Mode != "snapshot" {
			t.Fatalf("attached = %+v, want snapshot with vt built", a)
		}
		if snap := b.next(proto.TSnapshot).Payload; !bytes.Contains(snap, []byte("FIRST")) {
			t.Fatalf("snapshot = %q", snap)
		}
	} else {
		if a.Mode != "full" || a.From != 0 || a.Live != 6 {
			t.Fatalf("attached = %+v, want full ring from 0", a)
		}
		if got := b.output(0, "FIRST"); string(got) != "FIRST\n" {
			t.Fatalf("history = %q", got)
		}
	}

	b.send(proto.TInput, []byte("x"))
	if got := b.output(6, "SECOND"); !bytes.Equal(got, []byte("SECOND\x1b[?2004h\n")) {
		t.Fatalf("live output = %q", got)
	}

	// Second client replays from byte offset 6.
	b2 := newRawBridge(t)
	a2 := b2.attach(s.ID, 6, 80, 24)
	if a2.Mode != "replay" || a2.From != 6 {
		t.Fatalf("attached = %+v, want replay from 6", a2)
	}
	if got := b2.output(6, "SECOND"); !bytes.Equal(got, []byte("SECOND\x1b[?2004h\n")) {
		t.Fatalf("replay = %q", got)
	}

	// Resize is propagated through the link to the guest PTY and trusted manifest.
	b2.sendJSON(proto.TResize, proto.Resize{Cols: 132, Rows: 43})
	hosttest.WaitFor(t, 5*time.Second, "resized manifest", func() bool {
		m := live(t, s.ID)
		return m.Term.Cols == 132 && m.Term.Rows == 43
	})
	b2.send(proto.TInput, []byte("y"))
	var ended proto.Ended
	var tail []byte
	for {
		f := b2.next(proto.TOutput, proto.TEnded)
		if f.Type == proto.TEnded {
			_ = json.Unmarshal(f.Payload, &ended)
			break
		}
		_, data, _ := proto.DecodeOutput(f.Payload)
		tail = append(tail, data...)
	}
	if !bytes.Contains(tail, []byte("43 132")) || ended.ExitCode != 7 {
		t.Fatalf("tail=%q ended=%+v, want '43 132' and exit 7", tail, ended)
	}
	waitState(t, s.ID, host.StateErrored)
	list, err := host.List()
	if err != nil || len(list) != 1 || !list[0].Claimed || list[0].ExitCode == nil || *list[0].ExitCode != 7 {
		t.Fatalf("ended session row = %+v, %v", list, err)
	}
}

func TestWalledPlainHooksStatusAndAlerts(t *testing.T) {
	h := hosttest.New(t)
	fakeClaude(t, h, "exec /bin/sh")
	events := filepath.Join(h.Home, "walled-events")
	if _, err := host.AlertsSet(api.AlertsSetArgs{Config: api.AlertsConfig{
		Events:       []string{"needs_input", "failed", "started"},
		Delay:        "0s",
		Detail:       ptr(true),
		Destinations: []api.AlertDestination{{Type: "command", Run: "cat >> " + events + "; echo >> " + events}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Install fake claude so walledSearchPath sees it, and have fakeClaude write args to a fixed path.
	s, err := host.New(host.NewArgs{Agent: "claude", Name: "walled-hook", Cwd: "~", Wall: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	// Read the hook command from the manifest's agent hooks (guest HOME differs from host HOME).
	m0 := manifest(t, s.ID)
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if len(m0.Agent.Hooks) < 2 || json.Unmarshal([]byte(m0.Agent.Hooks[1]), &settings) != nil {
		t.Fatalf("unexpected claude hooks: %q", m0.Agent.Hooks)
	}
	hookCmd := func(ev string) string { return settings.Hooks[ev][0].Hooks[0].Command }

	const validID = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	send(t, s.ID, `echo '{"session_id":"--dangerously-skip-permissions","source":"startup"}' | `+hookCmd("SessionStart"))
	send(t, s.ID, `echo '{"session_id":"`+validID+`","source":"clear"}' | `+hookCmd("SessionStart"))
	hosttest.WaitFor(t, 10*time.Second, "valid agent_id propagated to supervisor", func() bool {
		a := live(t, s.ID).Agent
		return a.AgentSessionID == validID && a.IDSource == "hook"
	})

	send(t, s.ID, `echo '{"message":"Walled agent asks permission"}' | `+hookCmd("Notification"))
	m := waitStatus(t, s.ID, host.StateNeedsInput, "notify")
	if m.Status.Reason != "Walled agent asks permission" || !m.Status.Claimed {
		t.Fatalf("status = %+v, want claimed reason", m.Status)
	}
	hosttest.WaitFor(t, 10*time.Second, "needs_input alert from supervisor", func() bool {
		b, _ := os.ReadFile(events)
		return strings.Contains(string(b), "Walled agent asks permission") && strings.Contains(string(b), `"started"`)
	})

	send(t, s.ID, hookCmd("UserPromptSubmit")+" </dev/null")
	waitStatus(t, s.ID, host.StateWorking, "hook")
	send(t, s.ID, hookCmd("Stop")+" </dev/null")
	waitStatus(t, s.ID, host.StateIdle, "hook")
}

func TestWalledPlainImagePasteUpload(t *testing.T) {
	h := hosttest.New(t)
	s := startWalled(t, "paste", "sleep", "60")
	c := dial(t, h)
	if !c.HasFeature("put") {
		t.Fatalf("bridge lacks put feature: %q", c.Features)
	}
	data := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	guestPath, err := c.Upload(context.Background(), s.ID, "../../My Shot.png", data)
	if err != nil {
		t.Fatalf("client upload to walled session: %v", err)
	}
	if !strings.HasSuffix(guestPath, "-My_Shot.png") {
		t.Fatalf("guest upload path = %q", guestPath)
	}
	if !strings.HasPrefix(guestPath, paths.GuestRuntimeDir(s.ID)) {
		t.Fatalf("expected upload in guest runtime paste dir, got %q", guestPath)
	}
	got, err := os.ReadFile(guestPath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("uploaded file mismatch (%v)", err)
	}
	if fi, err := os.Stat(guestPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("uploaded file mode = %v, %v", fi, err)
	}
	// Host PasteDir must remain empty/unused.
	if _, err := os.Stat(paths.PasteDir(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("host paste dir should not be used for walled session: %v", err)
	}

	// Damaged upload over raw bridge is rejected.
	b := newRawBridge(t)
	sum := sha256.Sum256([]byte("hello"))
	_ = hex.EncodeToString(sum[:])
	b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: 5, Session: s.ID, Name: "bad.txt", Size: 5})
	b.send(proto.TUploadChunk, proto.EncodeChunk(5, []byte("hello")))
	b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: 5, SHA256: strings.Repeat("0", 64)})
	if r := b.response(5); r.OK || r.Error == nil || r.Error.Code != "upload" {
		t.Fatalf("damaged walled upload = %+v, want upload error", r)
	}
}

func TestWalledPlainSecretsInMemoryOnly(t *testing.T) {
	hosttest.New(t)
	const secretVal = "super-secret-value-never-on-disk-42"
	s, err := host.New(host.NewArgs{
		Name:    "secret-walled",
		Cwd:     "~",
		Wall:    "plain",
		Argv:    []string{"/bin/sh", "-c", `echo "GOT=$TEST_TOKEN"; exec sleep 60`},
		Secrets: map[string]api.Secret{"TEST_TOKEN": secretVal},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitOutput(t, s.ID, "GOT="+secretVal)

	// Walk both the host session dir and guest runtime dir; the secret value must never appear on disk.
	for _, root := range []string{paths.SessionDir(s.ID), paths.GuestRuntimeDir(s.ID)} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && bytes.Contains(b, []byte(secretVal)) {
				t.Errorf("secret value leaked to disk in %s", p)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

func TestWalledPlainResume(t *testing.T) {
	hosttest.New(t)
	s := startWalled(t, "resume-walled", "/bin/sh", "-c",
		`if [ -e "$HOME/ran" ]; then echo RESUMED-OK; exec sleep 60; fi; touch "$HOME/ran"; echo FIRST-RUN; exit 3`)
	waitOutput(t, s.ID, "FIRST-RUN")
	waitState(t, s.ID, host.StateErrored)
	oldPID := hosttest.HolderPID(t, s.ID)

	r, err := host.Resume(api.ResumeArgs{ID: s.ID})
	if err != nil {
		t.Fatalf("resume walled session: %v", err)
	}
	if !r.Session.Alive || r.Session.Wall == nil || r.Session.Wall.Driver != "plain" {
		t.Fatalf("resumed walled session = %+v", r.Session)
	}
	if err := hosttest.Reap(oldPID, 5*time.Second); err != nil {
		t.Fatalf("old supervisor still running: %v", err)
	}
	waitOutput(t, s.ID, "RESUMED-OK")
	if pid := hosttest.HolderPID(t, s.ID); pid == oldPID {
		t.Fatalf("resumed session reused old supervisor PID %d", pid)
	}
}

func TestNewWalledRejectsUnknownWall(t *testing.T) {
	hosttest.New(t)
	_, err := host.New(host.NewArgs{Name: "bad", Cwd: "~", Argv: []string{"true"}, Wall: "microvm"})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_unavailable" || pe.Hint == "" {
		t.Fatalf("new --wall microvm = %v, want wall_unavailable with hint", err)
	}
}
