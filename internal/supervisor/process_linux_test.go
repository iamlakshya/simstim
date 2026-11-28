//go:build linux

package supervisor

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestSupervisorProcessWallEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable on this host: %s (%s)", probe.Reason, probe.Fix)
	}

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	helperBin := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(filepath.Dir(helperBin), 0o700); err != nil {
		t.Fatal(err)
	}
	// Pin helperBin's version to the test binary's version.Version and disable
	// VCS stamping so internal/version.init() does not overwrite "0.0.0-dev" on
	// clean git checkouts (which would trip internal/link's version check).
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}

	wsHost := filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(wsHost, 0o700); err != nil {
		t.Fatal(err)
	}

	id := "pw01test"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "myrepo",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:       "/work/myrepo",
		Env: []string{
			"HOME=" + process.GuestHome,
			"USER=agent",
			"LOGNAME=agent",
			"LANG=C.UTF-8",
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"TERM=xterm-256color",
			"HTTP_PROXY=" + process.GuestProxyURL,
			"HTTPS_PROXY=" + process.GuestProxyURL,
			"NO_PROXY=localhost,127.0.0.1,::1",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         process.DriverName,
			Version:        probe.Version,
			Preset:         string(egress.PresetLocked),
			WorkspaceHost:  wsHost,
			WorkspaceGuest: "/work/myrepo",
			StateDirs:      []string{".claude/projects"},
			At:             now,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	runCycle := func(cmds string, expectContains []string) {
		t.Helper()
		m.Status = host.Status{State: host.StateStarting, Since: time.Now().UTC()}
		if err := m.Save(); err != nil {
			t.Fatalf("save manifest before cycle: %v", err)
		}
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				EgressExe:        helperBin,
			})
		}()

		// Wait for supervisor socket + running state.
		var conn net.Conn
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var derr error
			conn, derr = net.DialTimeout("unix", paths.Socket(id), time.Second)
			if derr == nil {
				if lm, lerr := host.LoadManifest(id); lerr == nil && lm.Status.State != host.StateStarting {
					break
				}
				_ = conn.Close()
				conn = nil
			}
			select {
			case err := <-supDone:
				t.Fatalf("supervisor exited early: %v", err)
			default:
			}
			time.Sleep(50 * time.Millisecond)
		}
		if conn == nil {
			t.Fatal("timed out waiting for supervisor socket")
		}

		// Perform Hello + Attach handshake and verify byte-exact I/O inside the process wall.
		w := proto.NewWriter(conn)
		rd := bufio.NewReaderSize(conn, 64<<10)
		if err := w.WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge",
		}); err != nil {
			t.Fatalf("send hello: %v", err)
		}
		hf, err := proto.Read(rd)
		if err != nil || hf.Type != proto.THello {
			t.Fatalf("read hello: frame=%v err=%v", hf.Type, err)
		}
		if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24}); err != nil {
			t.Fatalf("send attach: %v", err)
		}
		af, err := proto.Read(rd)
		if err != nil || af.Type != proto.TAttached {
			t.Fatalf("read attached: type=0x%x payload=%q err=%v", af.Type, string(af.Payload), err)
		}

		if err := w.Write(proto.TInput, []byte(cmds)); err != nil {
			t.Fatalf("write input: %v", err)
		}

		var out strings.Builder
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				fr, rerr := proto.Read(rd)
				if rerr != nil {
					return
				}
				if fr.Type == proto.TOutput {
					out.Write(fr.Payload)
					allFound := true
					cur := out.String()
					for _, want := range expectContains {
						if !strings.Contains(cur, want) {
							allFound = false
							break
						}
					}
					if allFound {
						return
					}
				}
			}
		}()

		select {
		case <-readDone:
		case <-time.After(15 * time.Second):
			_ = conn.Close()
			t.Fatalf("timed out waiting for expected output %v; got:\n%s", expectContains, out.String())
		}
		_ = conn.Close()

		// Clean stop via session socket.
		if _, err := host.HolderRequest(id, "kill", nil, 5*time.Second); err != nil {
			t.Fatalf("HolderRequest kill: %v", err)
		}

		select {
		case err := <-supDone:
			if err != nil {
				t.Fatalf("supervisor returned error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("supervisor did not exit after kill")
		}
	}

	// Cycle 1: verify guest paths, write kept/ephemeral/workspace files, test egress proxy (locked preset -> 403), and direct TCP fail-closed.
	cycle1Cmd := strings.Join([]string{
		`printf 'ENV:%s:%s:%s\n' "$HOME" "$USER" "$(pwd)"`,
		`echo WORK_KEPT > /work/myrepo/work.txt`,
		`echo STATE_KEPT > /home/agent/.claude/projects/kept.txt`,
		`echo EPHEMERAL > /home/agent/ephemeral.txt`,
		`python3 -c 'import socket; s=socket.create_connection(("127.0.0.1",3128),5); s.sendall(b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); print("PROXY:" + s.recv(128).decode(errors="replace").splitlines()[0]); s.close()'`,
		`python3 -c 'import socket,errno; s=socket.socket(); s.settimeout(1);
try: s.connect(("1.1.1.1",443)); print("DIRECT:OPEN")
except OSError as e: print("DIRECT:BLOCKED:" + str(e.errno))'`,
		`echo CYCLE1_DONE`,
	}, "\n") + "\n"

	runCycle(cycle1Cmd, []string{
		"ENV:/home/agent:agent:/work/myrepo",
		"PROXY:HTTP/1.1 403",
		"DIRECT:BLOCKED:",
		"CYCLE1_DONE",
	})

	// Verify audit.jsonl recorded the egress denial from the trusted egress process.
	auditBytes, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatalf("read audit.jsonl: %v", err)
	}
	if !strings.Contains(string(auditBytes), `"event":"egress"`) || !strings.Contains(string(auditBytes), "example.com") {
		t.Fatalf("audit.jsonl missing egress record for example.com:\n%s", string(auditBytes))
	}

	// Cycle 2 (stop + resume): fresh tmpfs drops ephemeral.txt, while workspace and kept StateDirs survive.
	cycle2Cmd := strings.Join([]string{
		`cat /work/myrepo/work.txt`,
		`cat /home/agent/.claude/projects/kept.txt`,
		`if [ -e /home/agent/ephemeral.txt ]; then echo EPHEMERAL_LEAKED; else echo EPHEMERAL_GONE; fi`,
		`echo CYCLE2_DONE`,
	}, "\n") + "\n"

	runCycle(cycle2Cmd, []string{
		"WORK_KEPT",
		"STATE_KEPT",
		"EPHEMERAL_GONE",
		"CYCLE2_DONE",
	})
}
