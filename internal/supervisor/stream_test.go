package supervisor_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supervisor"
)

// inProcSandbox runs guest.RunConn in-process over a net.Pipe for fast,
// cross-platform supervisor stream tests.
type inProcSandbox struct {
	runDir string
	done   chan supervisor.Exit
}

func (s *inProcSandbox) Start(_ context.Context) (net.Conn, error) {
	supConn, guestConn := net.Pipe()
	s.done = make(chan supervisor.Exit, 1)
	go func() { //nolint:contextcheck // guest.RunConn takes no context
		err := guest.RunConn(guestConn, s.runDir)
		_ = guestConn.Close()
		s.done <- supervisor.Exit{Err: err}
	}()
	return supConn, nil
}
func (s *inProcSandbox) Stop(_ context.Context, _ time.Duration) error { return nil }
func (s *inProcSandbox) Kill() error                                   { return nil }
func (s *inProcSandbox) Destroy() error                                { return nil }
func (s *inProcSandbox) Wait() supervisor.Exit                         { return <-s.done }

func TestSupervisorBridgeWalledSFTPAndExec(t *testing.T) {
	th := hosttest.New(t)
	h, tr := th.ClientHost("box")

	work := filepath.Join(th.Home, "mywork")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("inside-wall\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const id = "wstr2345"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "mywork",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh", "-c", "sleep 60"}},
		Cwd:       work,
		Env:       []string{"HOME=" + th.Home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Term:      host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Run:       host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status:    host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         "plain",
			WorkspaceHost:  work,
			WorkspaceGuest: work,
		},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	sb := &inProcSandbox{runDir: paths.GuestRuntimeDir(id)}
	supDone := make(chan error, 1)
	go func() {
		supDone <- supervisor.RunWith(id, sb, supervisor.Options{
			HeartbeatTimeout: 10 * time.Second,
			TickInterval:     200 * time.Millisecond,
			StopGrace:        time.Second,
		})
	}()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		select {
		case <-supDone:
		case <-time.After(5 * time.Second):
		}
		// Remove session dir before hosttest.cleanup runs: in-process
		// supervisor.RunWith recorded os.Getpid() as HolderPID.
		_ = os.RemoveAll(paths.SessionDir(id))
	})

	hosttest.WaitFor(t, 10*time.Second, "supervisor running", func() bool {
		lm, err := host.LoadManifest(id)
		return err == nil && lm.Status.State == host.StateWorking
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. Copy files and folders in and out over walled SFTP (bridge -> supervisor -> guest init).
	sess := &client.Session{ID: id, Name: "mywork", Cwd: work, Wall: &api.WallInfo{Level: "process"}}
	sc, closer, err := client.OpenSessionSFTP(ctx, h, sess)
	if err != nil {
		t.Fatalf("OpenSessionSFTP(walled): %v", err)
	}
	wd, err := sc.Getwd()
	if err != nil || wd != work {
		t.Fatalf("sftp Getwd = %q (%v), want %q", wd, err, work)
	}
	localDir := t.TempDir()
	resOut, err := client.CopyFromSFTP(sc, "hello.txt", localDir, client.CopyOptions{})
	if err != nil || resOut.Files != 1 {
		t.Fatalf("CopyFromSFTP = %+v, %v", resOut, err)
	}
	got, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil || string(got) != "inside-wall\n" {
		t.Fatalf("copied out = %q, %v", got, err)
	}

	// Copy a folder recursively into the walled session.
	srcFolder := filepath.Join(localDir, "bundle")
	if err := os.MkdirAll(filepath.Join(srcFolder, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcFolder, "nested", "a.txt"), []byte("nested-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resIn, err := client.CopyToSFTP(sc, srcFolder, "bundle", client.CopyOptions{})
	if err != nil || resIn.Files != 1 {
		t.Fatalf("CopyToSFTP folder = %+v, %v", resIn, err)
	}
	_ = closer.Close()

	// 2. Run SessionSSHBackend.OpenSFTP (raw SFTP stream) and StartExec (pipe & PTY) through the supervisor.
	backend := &client.SessionSSHBackend{Host: h, SessionID: id, Transport: tr, Walled: true}

	rawSFTP, err := backend.OpenSFTP(ctx)
	if err != nil {
		t.Fatalf("backend.OpenSFTP(walled): %v", err)
	}
	sc2, err := sftp.NewClientPipe(rawSFTP, rawSFTP)
	if err != nil {
		_ = rawSFTP.Close()
		t.Fatalf("sftp.NewClientPipe on walled stream: %v", err)
	}
	rf, err := sc2.Open("bundle/nested/a.txt")
	if err != nil {
		t.Fatalf("open uploaded nested file: %v", err)
	}
	nestedGot, _ := io.ReadAll(rf)
	_ = rf.Close()
	_ = sc2.Close()
	_ = rawSFTP.Close()
	if string(nestedGot) != "nested-data\n" {
		t.Fatalf("nested file = %q, want nested-data\\n", nestedGot)
	}

	// Pipe exec: separate stdout, stderr, stdin EOF, non-zero exit code.
	es, err := backend.StartExec(ctx, api.SSHExecArgs{Command: "cat; cat bundle/nested/a.txt; echo err-msg >&2; exit 9"})
	if err != nil {
		t.Fatalf("backend.StartExec(walled pipe): %v", err)
	}
	_, _ = es.Stdin().Write([]byte("stdin-line\n"))
	_ = es.Stdin().Close()
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(es.Stdout()); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(es.Stderr()); errCh <- string(b) }()
	code, err := es.Wait()
	_ = es.Close()
	if err != nil || code != 9 {
		t.Fatalf("exec exit = %d (%v), want 9", code, err)
	}
	if gotOut, gotErr := <-outCh, <-errCh; gotOut != "stdin-line\nnested-data\n" || gotErr != "err-msg\n" {
		t.Fatalf("exec stdout=%q stderr=%q", gotOut, gotErr)
	}

	// PTY exec inside walled session.
	ptyExec, err := backend.StartExec(ctx, api.SSHExecArgs{Command: "pwd", PTY: true, Term: "xterm-256color", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("backend.StartExec(walled pty): %v", err)
	}
	ptyOut, _ := io.ReadAll(ptyExec.Stdout())
	ptyCode, _ := ptyExec.Wait()
	_ = ptyExec.Close()
	if ptyCode != 0 || !strings.Contains(string(ptyOut), work) {
		t.Fatalf("pty exec out=%q code=%d, want %q", ptyOut, ptyCode, work)
	}

	// Client closing a long-running exec stream terminates the guest command promptly.
	longExec, err := backend.StartExec(ctx, api.SSHExecArgs{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("backend.StartExec(long): %v", err)
	}
	startClose := time.Now()
	_ = longExec.Close()
	if elapsed := time.Since(startClose); elapsed > 4*time.Second {
		t.Fatalf("closing long exec took %v", elapsed)
	}

	// Verify audit log recorded sftp_open and exec_open.
	auditBytes, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatalf("read audit.jsonl: %v", err)
	}
	for _, want := range []string{`"event":"sftp_open"`, `"event":"exec_open"`} {
		if !strings.Contains(string(auditBytes), want) {
			t.Errorf("audit.jsonl missing %s:\n%s", want, auditBytes)
		}
	}
}
