package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// TestSupervisorMultiSessionEventsIsolation verifies spec 10.3: two sessions in
// one sandbox keep independent state, agent_id and ended claims, and foreign
// session events are dropped.
func TestSupervisorMultiSessionEventsIsolation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	now := time.Now().UTC()
	pID, cID := "prim1111", "chld2222"
	pm := &host.Manifest{
		Schema: 1, ID: pID, Name: "app", CreatedAt: now,
		Agent:  host.AgentInfo{Kind: "claude", Argv: []string{"claude"}},
		Cwd:    "/work/app",
		Status: host.Status{State: host.StateWorking, Since: now},
		Wall:   &host.WallRecord{Level: "process", Driver: "process"},
	}
	cm := &host.Manifest{
		Schema: 1, ID: cID, Name: "app-codex", SandboxOf: pID, CreatedAt: now,
		Agent:  host.AgentInfo{Kind: "codex", Argv: []string{"codex"}},
		Cwd:    "/work/app",
		Status: host.Status{State: host.StateWorking, Since: now},
		Wall:   &host.WallRecord{Level: "process", Driver: "process"},
	}
	if err := pm.Save(); err != nil {
		t.Fatal(err)
	}
	if err := cm.Save(); err != nil {
		t.Fatal(err)
	}

	aud, err := openAuditLog(paths.AuditLog(pID))
	if err != nil {
		t.Fatal(err)
	}
	defer aud.Close()
	lf, err := host.OpenRotatingLog(paths.SupervisorLog(pID))
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	s := &Supervisor{
		id:    pID,
		m:     pm,
		log:   host.NewSessionLogger(lf),
		audit: aud,
		sessions: map[string]*sandboxSession{
			pID: {id: pID, m: pm},
			cID: {id: cID, m: cm},
		},
	}

	s.applyGuestEvent(guest.Event{Type: "status", Session: pID, State: host.StateIdle, Source: "hooks"})
	s.applyGuestEvent(guest.Event{Type: "status", Session: cID, State: host.StateNeedsInput, Source: "hooks", Reason: "confirm"})
	s.applyGuestEvent(guest.Event{Type: "agent_id", Session: pID, AgentID: "11111111-1111-1111-1111-111111111111"})
	s.applyGuestEvent(guest.Event{Type: "agent_id", Session: cID, AgentID: "22222222-2222-2222-2222-222222222222"})
	s.applyGuestEvent(guest.Event{Type: "ended", Session: cID, ExitCode: 0})
	s.applyGuestEvent(guest.Event{Type: "status", Session: "foreign99", State: host.StateErrored, Source: "hooks"})

	if pm.Status.State != host.StateIdle || pm.Agent.AgentSessionID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("primary manifest corrupted: state=%s id=%s", pm.Status.State, pm.Agent.AgentSessionID)
	}
	if cm.Status.State != host.StateExited || !cm.Status.Claimed || cm.Agent.AgentSessionID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("child manifest mismatch: state=%s claimed=%v id=%s", cm.Status.State, cm.Status.Claimed, cm.Agent.AgentSessionID)
	}
	if pm.Status.State == host.StateExited {
		t.Fatal("child ended event affected primary state")
	}

	// Verify recordExtraAgent copy-on-write does not race with concurrent readers
	// shallow-copying *s.m and marshaling Wall outside s.mu.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			s.mu.Lock()
			cp := *s.m
			s.mu.Unlock()
			_, _ = json.Marshal(cp)
		}
	}()
	for _, kind := range []string{"codex", "gemini", "opencode", "aider"} {
		s.recordExtraAgent(kind)
	}
	<-done
	if len(s.m.Wall.ExtraAgents) != 4 {
		t.Fatalf("ExtraAgents = %v, want 4 entries", s.m.Wall.ExtraAgents)
	}
}

func TestSupervisorExecOverBridgeLinkStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	helperBin := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(filepath.Dir(helperBin), 0o700); err != nil {
		t.Fatal(err)
	}
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}

	id := "ex23test"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema: 1, ID: id, Name: "box-app", CreatedAt: now,
		Agent:  host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:    tmp,
		Env:    []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + tmp},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall:   &host.WallRecord{Level: "none", Driver: "plain", Version: version.Version, At: now},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	sb := NewPlainSandbox(id, helperBin, filepath.Join(tmp, "guest-run"))
	supDone := make(chan error, 1)
	go func() {
		supDone <- RunWith(id, sb, Options{
			HeartbeatTimeout: 10 * time.Second,
			TickInterval:     100 * time.Millisecond,
			StopGrace:        2 * time.Second,
		})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", paths.Socket(id), 200*time.Millisecond); err == nil {
			_ = c.Close()
			if lm, lerr := host.LoadManifest(id); lerr == nil && lm.Status.State != host.StateStarting {
				break
			}
		}
		select {
		case err := <-supDone:
			t.Fatalf("supervisor exited early: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}

	runExec := func(argv []string, timeoutMS int64, noTimeout bool) (int, string, string) {
		t.Helper()
		conn, err := net.DialTimeout("unix", paths.Socket(id), 3*time.Second)
		if err != nil {
			t.Fatalf("dial supervisor: %v", err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Fatal(err)
		}
		w := proto.NewWriter(conn)
		r := bufio.NewReaderSize(conn, 64<<10)
		// Send a different helper version to verify AllowVersionMismatch (spec 10.5).
		if err := w.WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: "0.0.1-other", Role: "bridge",
		}); err != nil {
			t.Fatal(err)
		}
		if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
			t.Fatalf("hello failed: %v", err)
		}
		argsBytes, _ := json.Marshal(api.ExecArgs{ID: id, Argv: argv, Cwd: tmp, TimeoutMS: timeoutMS, NoTimeout: noTimeout})
		if err := w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: argsBytes}); err != nil {
			t.Fatal(err)
		}
		rf, err := proto.Read(r)
		if err != nil || rf.Type != proto.TResponse {
			t.Fatalf("exec response failed: %v", err)
		}
		var resp proto.Response
		if err := json.Unmarshal(rf.Payload, &resp); err != nil || !resp.OK {
			t.Fatalf("exec response error: %+v", resp.Error)
		}
		ls := link.New(&bufferedConn{Conn: conn, r: r}, link.Guest, link.Options{
			Opener: link.BridgeOpener, AllowVersionMismatch: true,
		})
		defer ls.Close()
		st, err := ls.Accept(ctx)
		if err != nil {
			t.Fatalf("accept exec stream: %v", err)
		}
		defer st.Close()
		_ = st.CloseWrite()
		var out, errOut bytes.Buffer
		code, err := api.ReadExecStream(st, &out, &errOut)
		if err != nil {
			t.Fatalf("ReadExecStream: %v", err)
		}
		return code, out.String(), errOut.String()
	}

	if code, out, errOut := runExec([]string{"/bin/sh", "-c", "echo alpha; echo beta >&2"}, 5000, false); code != 0 || strings.TrimSpace(out) != "alpha" || strings.TrimSpace(errOut) != "beta" {
		t.Fatalf("exec alpha/beta = (%d, %q, %q)", code, out, errOut)
	}
	if code, out, _ := runExec([]string{"/bin/sh", "-c", "echo no-timeout-ok"}, 0, true); code != 0 || strings.TrimSpace(out) != "no-timeout-ok" {
		t.Fatalf("exec NoTimeout = (%d, %q)", code, out)
	}
	if code, _, _ := runExec([]string{"/bin/sh", "-c", "false"}, 5000, false); code != 1 {
		t.Fatalf("false exit = %d, want 1", code)
	}
	if code, _, _ := runExec([]string{"no-such-cmd-xyz"}, 5000, false); code != 127 {
		t.Fatalf("missing exit = %d, want 127", code)
	}
	if code, _, _ := runExec([]string{"/bin/sh", "-c", "sleep 20"}, 150, false); code != 124 {
		t.Fatalf("timeout exit = %d, want 124", code)
	}

	_, _ = host.HolderRequest(id, "stop", nil, 2*time.Second)
	select {
	case <-supDone:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}
