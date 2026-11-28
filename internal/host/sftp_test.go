package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func saveTestManifest(t *testing.T, id, cwd string) {
	t.Helper()
	alertHome(t)
	fakeProc(t, map[string]string{"self/cgroup": outsideCgroup("")})
	userManager(t, "cgroup:[1]", "cgroup:[1]", nil)
	m := &Manifest{
		Schema:    1,
		ID:        id,
		Name:      "test-sess",
		CreatedAt: time.Now().UTC(),
		Cwd:       cwd,
		Agent:     AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSFTP(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("from-host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const id = "sftptest"
	saveTestManifest(t, id, work)

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer c2sR.Close()
		defer s2cW.Close()
		errCh <- RunSFTP(id, c2sR, s2cW)
	}()

	cli, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	wd, err := cli.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if wd != work {
		t.Errorf("wd = %q, want %q", wd, work)
	}
	rf, err := cli.Open("hello.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil || string(got) != "from-host\n" {
		t.Fatalf("read = %q, %v", got, err)
	}
	wf, err := cli.Create("pushed.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := wf.Write([]byte("from-laptop\n")); err != nil {
		t.Fatal(err)
	}
	_ = wf.Close()
	_ = cli.Close()
	_ = c2sW.Close()
	if err := <-errCh; err != nil {
		t.Fatalf("RunSFTP: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(work, "pushed.txt"))
	if err != nil || string(b) != "from-laptop\n" {
		t.Fatalf("pushed.txt = %q, %v", b, err)
	}
}

func TestRunDialPort(t *testing.T) {
	work := t.TempDir()
	const id = "porttest"
	saveTestManifest(t, id, work)

	// Non-loopback must be refused.
	var out bytes.Buffer
	if err := RunDialPort([]string{id, "192.0.2.1", "80"}, strings.NewReader(""), &out); err == nil {
		t.Fatal("expected refusal for non-loopback address")
	}
	if !strings.Contains(out.String(), "not loopback") {
		t.Errorf("reply = %q, want not loopback", out.String())
	}

	// Loopback echo server works.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer c2sR.Close()
		defer s2cW.Close()
		done <- RunDialPort([]string{id, "localhost", portStr}, c2sR, s2cW)
	}()
	br := bufio.NewReader(s2cR)
	line, err := br.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var rep api.DialPortReply
	if err := json.Unmarshal(line, &rep); err != nil || !rep.OK {
		t.Fatalf("reply = %s (%v)", line, err)
	}
	go func() {
		_, _ = c2sW.Write([]byte("ping"))
		_ = c2sW.Close()
	}()
	echoed, err := io.ReadAll(br)
	if err != nil || string(echoed) != "ping" {
		t.Fatalf("echo = %q, %v", echoed, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("RunDialPort: %v", err)
	}
}

func TestRunSSHExecPipeAndPTY(t *testing.T) {
	work := t.TempDir()
	const id = "exectest"
	saveTestManifest(t, id, work)

	// 1. Pipe mode: separate stdout, stderr, stdin EOF, and non-zero exit code.
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer c2sR.Close()
		defer s2cW.Close()
		errCh <- RunSSHExec(id, c2sR, s2cW)
	}()
	w := proto.NewWriter(c2sW)
	r := bufio.NewReader(s2cR)
	argsBytes, _ := json.Marshal(api.SSHExecArgs{
		Command: "cat; echo err-line >&2; exit 7",
	})
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: argsBytes}); err != nil {
		t.Fatal(err)
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TResponse {
		t.Fatalf("first frame = %v, %v", f.Type, err)
	}
	_ = w.Write(proto.TInput, []byte("from-stdin\n"))
	_ = w.Write(proto.TInput, nil) // EOF

	var stdout, stderr bytes.Buffer
	var ended proto.Ended
	for {
		f, err := proto.Read(r)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch f.Type {
		case proto.TOutput:
			if len(f.Payload) > 1 {
				switch f.Payload[0] {
				case SSHStreamStdout:
					stdout.Write(f.Payload[1:])
				case SSHStreamStderr:
					stderr.Write(f.Payload[1:])
				}
			}
		case proto.TEnded:
			_ = json.Unmarshal(f.Payload, &ended)
			goto donePipe
		default:
		}
	}
donePipe:
	_ = c2sW.Close()
	if err := <-errCh; err != nil {
		t.Fatalf("RunSSHExec pipe: %v", err)
	}
	if stdout.String() != "from-stdin\n" || stderr.String() != "err-line\n" || ended.ExitCode != 7 {
		t.Errorf("stdout=%q stderr=%q exit=%d", stdout.String(), stderr.String(), ended.ExitCode)
	}

	// 2. PTY mode: runs in work dir with resize.
	c2sR2, c2sW2 := io.Pipe()
	s2cR2, s2cW2 := io.Pipe()
	errCh2 := make(chan error, 1)
	go func() {
		defer c2sR2.Close()
		defer s2cW2.Close()
		errCh2 <- RunSSHExec(id, c2sR2, s2cW2)
	}()
	w2 := proto.NewWriter(c2sW2)
	r2 := bufio.NewReader(s2cR2)
	ptyArgs, _ := json.Marshal(api.SSHExecArgs{Command: "pwd", PTY: true, Term: "xterm-256color", Cols: 100, Rows: 30})
	_ = w2.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: ptyArgs})
	if f, err := proto.Read(r2); err != nil || f.Type != proto.TResponse {
		t.Fatalf("pty response = %v, %v", f.Type, err)
	}
	_ = w2.WriteJSON(proto.TResize, proto.Resize{Cols: 120, Rows: 40})
	var ptyOut bytes.Buffer
	var ptyEnded proto.Ended
	for {
		f, err := proto.Read(r2)
		if err != nil {
			t.Fatalf("pty read: %v", err)
		}
		if f.Type == proto.TOutput && len(f.Payload) > 1 {
			ptyOut.Write(f.Payload[1:])
		}
		if f.Type == proto.TEnded {
			_ = json.Unmarshal(f.Payload, &ptyEnded)
			break
		}
	}
	_ = c2sW2.Close()
	if err := <-errCh2; err != nil {
		t.Fatalf("RunSSHExec pty: %v", err)
	}
	if !strings.Contains(ptyOut.String(), work) || ptyEnded.ExitCode != 0 {
		t.Errorf("ptyOut=%q exit=%d, want %q", ptyOut.String(), ptyEnded.ExitCode, work)
	}
}

func TestBundleCreateIncrementalFullAndEmpty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "a.txt")
	gitRun("commit", "-q", "-m", "first")
	base := gitRun("rev-parse", "HEAD")

	// Same base => empty bundle.
	emptyRes, err := bundleCreate(&bundleCreateArgs{Dir: repo, Branch: "main", Base: base})
	if err != nil {
		t.Fatalf("empty bundleCreate: %v", err)
	}
	if !emptyRes.Empty || emptyRes.Head != base {
		t.Errorf("emptyRes = %+v, want Empty=true Head=%s", emptyRes, base)
	}

	// Second commit => incremental bundle.
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "b.txt")
	gitRun("commit", "-q", "-m", "second")
	head := gitRun("rev-parse", "HEAD")

	incRes, err := bundleCreate(&bundleCreateArgs{Dir: repo, Branch: "main", Base: base})
	if err != nil {
		t.Fatalf("inc bundleCreate: %v", err)
	}
	defer os.Remove(incRes.Path)
	if incRes.Full || incRes.Empty || incRes.Head != head || incRes.Size == 0 {
		t.Errorf("incRes = %+v", incRes)
	}
	// Tiny max_bytes triggers pull_too_big.
	if _, err := bundleCreate(&bundleCreateArgs{Dir: repo, Branch: "main", Full: true, MaxBytes: 10}); err == nil || !strings.Contains(err.Error(), "above the") {
		t.Errorf("expected pull_too_big, got %v", err)
	}
	// Invalid Base commit OIDs and invalid branch names are rejected before git runs.
	for _, badBase := range []string{"--upload-pack=evil", "not-a-hex-sha", "HEAD~1"} {
		if _, err := bundleCreate(&bundleCreateArgs{Dir: repo, Branch: "main", Base: badBase}); err == nil || !strings.Contains(err.Error(), "invalid base") {
			t.Errorf("bundleCreate(Base=%q) = %v, want invalid base error", badBase, err)
		}
	}
	for _, badBranch := range []string{"-flag", "a:b", "a..b"} {
		if _, err := bundleCreate(&bundleCreateArgs{Dir: repo, Branch: badBranch}); err == nil {
			t.Errorf("bundleCreate(Branch=%q) succeeded, want error", badBranch)
		}
	}
	_ = strconv.Itoa(0)
}

func TestSFTPAndExecRefuseInsideSession(t *testing.T) {
	alertHome(t)
	fakeProc(t, map[string]string{"self/cgroup": ownCgroup("abcd2345")})
	var out bytes.Buffer
	if err := RunSFTP("abcd2345", strings.NewReader(""), &out); !strings.Contains(err.Error(), "inside a session") {
		t.Errorf("RunSFTP inside session: %v", err)
	}
	if err := RunSSHExec("abcd2345", strings.NewReader(""), &out); !strings.Contains(err.Error(), "inside a session") {
		t.Errorf("RunSSHExec inside session: %v", err)
	}
	if err := RunDialPort([]string{"abcd2345", "127.0.0.1", "80"}, strings.NewReader(""), &out); !strings.Contains(err.Error(), "inside a session") {
		t.Errorf("RunDialPort inside session: %v", err)
	}
	if err := RunPullBundle("abcd2345", strings.NewReader(""), &out); !strings.Contains(err.Error(), "inside a session") {
		t.Errorf("RunPullBundle inside session: %v", err)
	}
}
