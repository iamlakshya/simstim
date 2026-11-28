//go:build linux

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
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// TestProcessWallShellExecMultiSessionAndKeep verifies P1.13 end to end inside
// a real bubblewrap process wall:
//   - shell / spawn runs as uid 1000 (agent) in /work/<repo> and shares files
//   - two sessions in one sandbox attach concurrently with independent screens
//   - exec separates stdout/stderr, returns 1 / 124 / 127, and sees the same 403 egress policy
//   - kill on a child ends only that child while primary keeps running
//   - keep files (.bash_history) persist across clean stop and restart
func TestProcessWallShellExecMultiSessionAndKeep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable on this host: %s (%s)", probe.Reason, probe.Fix)
	}

	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", runDir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	helperBin := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(filepath.Dir(helperBin), 0o700); err != nil {
		t.Fatal(err)
	}
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}

	wsHost := filepath.Join(tmp, "demoapp")
	if err := os.MkdirAll(wsHost, 0o700); err != nil {
		t.Fatal(err)
	}

	primaryID := "pw23prma"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        primaryID,
		Name:      "demoapp",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:       "/work/demoapp",
		Env: []string{
			"HOME=" + process.GuestHome,
			"USER=agent",
			"LOGNAME=agent",
			"LANG=C.UTF-8",
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"TERM=xterm-256color",
			"HTTP_PROXY=" + process.GuestProxyURL,
			"HTTPS_PROXY=" + process.GuestProxyURL,
			"http_proxy=" + process.GuestProxyURL,
			"https_proxy=" + process.GuestProxyURL,
			"NO_PROXY=localhost,127.0.0.1,::1",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(primaryID), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         process.DriverName,
			Version:        probe.Version,
			Preset:         string(egress.PresetLocked),
			WorkspaceHost:  wsHost,
			WorkspaceGuest: "/work/demoapp",
			StateDirs:      []string{".claude/projects", ".codex"},
			Keep:           []string{".bash_history"},
			At:             now,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	startSup := func() chan error {
		m.Status = host.Status{State: host.StateStarting, Since: time.Now().UTC()}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		sb := NewProcessSandbox(primaryID, helperBin, m)
		done := make(chan error, 1)
		go func() {
			done <- RunWith(primaryID, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				EgressExe:        helperBin,
			})
		}()
		t.Cleanup(func() {
			select {
			case <-done:
				return
			default:
			}
			_, _ = host.HolderRequest(primaryID, "stop", nil, 2*time.Second)
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				_ = sb.Kill()
			}
		})
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if c, err := net.DialTimeout("unix", paths.Socket(primaryID), 500*time.Millisecond); err == nil {
				_ = c.Close()
				if lm, lerr := host.LoadManifest(primaryID); lerr == nil && lm.Status.State != host.StateStarting {
					return done
				}
			}
			select {
			case err := <-done:
				t.Fatalf("supervisor exited early: %v", err)
			default:
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("supervisor did not become ready")
		return done
	}

	runExec := func(sock string, argv []string, timeoutMS int64) (int, string, string) {
		t.Helper()
		conn, err := net.DialTimeout("unix", sock, 3*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", sock, err)
		}
		defer conn.Close()
		w := proto.NewWriter(conn)
		r := bufio.NewReaderSize(conn, 64<<10)
		_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
		if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
			t.Fatalf("hello: %v", err)
		}
		argsB, _ := json.Marshal(api.ExecArgs{ID: primaryID, Argv: argv, Cwd: "/work/demoapp", TimeoutMS: timeoutMS})
		_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: argsB})
		rf, err := proto.Read(r)
		if err != nil || rf.Type != proto.TResponse {
			t.Fatalf("exec resp: %v", err)
		}
		var resp proto.Response
		if err := json.Unmarshal(rf.Payload, &resp); err != nil || !resp.OK {
			t.Fatalf("exec failed: %+v", resp.Error)
		}
		ls := link.New(&bufferedConn{Conn: conn, r: r}, link.Guest, link.Options{Opener: link.BridgeOpener, AllowVersionMismatch: true})
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

	supDone := startSup()

	// 1. Spawn a child shell inside the running sandbox (no restart).
	childID := "pw23sh22"
	cm := host.Manifest{
		Schema: 1, ID: childID, Name: "demoapp-sh", SandboxOf: primaryID, CreatedAt: time.Now().UTC(),
		Agent:  host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:    "/work/demoapp",
		Env:    []string{"TERM=xterm-256color"},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: time.Now().UTC()},
	}
	if _, err := host.HolderRequest(primaryID, "spawn", host.SpawnSocketRequest{Manifest: cm}, 15*time.Second); err != nil {
		t.Fatalf("spawn child shell: %v", err)
	}

	// 2. Two independent sessions attached at once get separate screens, and share /work/demoapp!
	attachAndCheck := func(sockID, cmdStr, want string) {
		t.Helper()
		conn, err := net.DialTimeout("unix", paths.Socket(sockID), 3*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", sockID, err)
		}
		defer conn.Close()
		w := proto.NewWriter(conn)
		r := bufio.NewReaderSize(conn, 64<<10)
		_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
		_, _ = proto.Read(r)
		_ = w.WriteJSON(proto.TAttach, proto.Attach{Session: sockID, Cols: 80, Rows: 24})
		_ = w.Write(proto.TInput, []byte(cmdStr+"\n"))
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var out strings.Builder
		for {
			f, err := proto.Read(r)
			if err != nil {
				t.Fatalf("attach %s read (%q): %v", sockID, out.String(), err)
			}
			if f.Type == proto.TOutput || f.Type == proto.TSnapshot {
				out.Write(f.Payload)
				if strings.Contains(out.String(), want) {
					return
				}
			}
		}
	}
	// Child shell writes a file in /work/demoapp and verifies uid=1000 and /work/demoapp.
	attachAndCheck(childID, `id -u; pwd; echo SHARED_FROM_SHELL > /work/demoapp/shared.txt; echo CHILD_D""ONE`, "CHILD_DONE")
	// Primary session reads the file written by the child shell!
	attachAndCheck(primaryID, `cat /work/demoapp/shared.txt`, "SHARED_FROM_SHELL")

	// 3. Exec verifies uid 1000, stdout/stderr separation, exit codes 1/124/127, and 403 egress block!
	code, out, errOut := runExec(paths.Socket(primaryID), []string{"/bin/sh", "-c", "id -u; pwd; echo ERR_LINE >&2"}, 5000)
	if code != 0 || !strings.Contains(out, "1000") || !strings.Contains(out, "/work/demoapp") || !strings.Contains(errOut, "ERR_LINE") {
		t.Fatalf("exec identity/streams = (%d, %q, %q)", code, out, errOut)
	}
	if code, _, _ := runExec(paths.Socket(primaryID), []string{"/bin/sh", "-c", "false"}, 5000); code != 1 {
		t.Fatalf("exec false = %d, want 1", code)
	}
	if code, _, _ := runExec(paths.Socket(primaryID), []string{"no-such-command-xyz"}, 5000); code != 127 {
		t.Fatalf("exec missing = %d, want 127", code)
	}
	if code, _, _ := runExec(paths.Socket(primaryID), []string{"/bin/sh", "-c", "sleep 20"}, 200); code != 124 {
		t.Fatalf("exec timeout = %d, want 124", code)
	}
	// Denied egress host via guest proxy gets HTTP 403 from egress (same as primary agent).
	if _, err := exec.LookPath("python3"); err == nil {
		code, httpOut, errOut := runExec(paths.Socket(primaryID), []string{
			"python3", "-c",
			`import socket; s=socket.create_connection(("127.0.0.1",3128),5); s.sendall(b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); print(s.recv(128).decode(errors="replace").splitlines()[0]); s.close()`,
		}, 8000)
		if code != 0 || !strings.Contains(httpOut, "403") {
			t.Fatalf("egress check from exec got (%d, %q, %q), want HTTP 403", code, httpOut, errOut)
		}
	}

	// 4. Write kept .bash_history inside guest home, kill child ONLY (primary keeps running!).
	if code, _, _ := runExec(paths.Socket(primaryID), []string{"/bin/sh", "-c", "echo KEEP_ME_HIST > /home/agent/.bash_history"}, 5000); code != 0 {
		t.Fatalf("write keep file exit = %d", code)
	}
	if _, err := host.HolderRequest(childID, "kill", nil, 3*time.Second); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	// Primary must still answer exec!
	if code, out, _ := runExec(paths.Socket(primaryID), []string{"/bin/sh", "-c", "echo STILL_ALIVE"}, 5000); code != 0 || !strings.Contains(out, "STILL_ALIVE") {
		t.Fatalf("primary did not stay alive after child kill: (%d, %q)", code, out)
	}

	// 5. Stop cleanly, restart sandbox, and verify .bash_history persisted across restarts!
	_, _ = host.HolderRequest(primaryID, "stop", nil, 3*time.Second)
	select {
	case <-supDone:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop cleanly")
	}

	supDone2 := startSup()
	code, out, _ = runExec(paths.Socket(primaryID), []string{"cat", "/home/agent/.bash_history"}, 5000)
	if code != 0 || strings.TrimSpace(out) != "KEEP_ME_HIST" {
		t.Fatalf("kept .bash_history after restart = (%d, %q), want KEEP_ME_HIST", code, out)
	}
	_, _ = host.HolderRequest(primaryID, "stop", nil, 3*time.Second)
	select {
	case <-supDone2:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor 2 did not stop cleanly")
	}
}
