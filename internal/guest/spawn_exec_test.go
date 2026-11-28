package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func startTestGuestRunner(t *testing.T, runDir string) (*link.Session, *link.Stream, *bufio.Reader, *proto.Writer, func()) {
	t.Helper()
	supConn, guestConn := net.Pipe()
	if err := supConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunConn(guestConn, runDir)
	}()

	sup := link.New(supConn, link.Supervisor, link.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctl, err := sup.Open(ctx, link.KindCtl, nil)
	if err != nil {
		_ = sup.Close()
		t.Fatalf("open ctl: %v", err)
	}
	w := proto.NewWriter(ctl)
	r := bufio.NewReaderSize(ctl, 64<<10)
	cleanup := func() {
		_ = ctl.Close()
		_ = sup.Close()
		_ = supConn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	return sup, ctl, r, w, cleanup
}

func ctlReq(t *testing.T, r *bufio.Reader, w *proto.Writer, id uint64, op string, args any) proto.Response {
	t.Helper()
	b, _ := json.Marshal(args)
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: op, Args: b}); err != nil {
		t.Fatalf("write ctl %s: %v", op, err)
	}
	for {
		f, err := proto.Read(r)
		if err != nil {
			t.Fatalf("read ctl reply for %s: %v", op, err)
		}
		if f.Type == proto.TResponse {
			var resp proto.Response
			if err := json.Unmarshal(f.Payload, &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.ID == id {
				return resp
			}
		}
	}
}

func TestGuestExecSemanticsAndClientCloseKill(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	sup, _, ctlR, ctlW, cleanup := startTestGuestRunner(t, runDir)
	defer cleanup()

	runExec := func(id string, argv []string, timeoutMS int64, stdinData string) (int, string, string) {
		t.Helper()
		resp := ctlReq(t, ctlR, ctlW, 1, "exec", ExecReq{
			ID:        id,
			Argv:      argv,
			Cwd:       tmp,
			TimeoutMS: timeoutMS,
		})
		if !resp.OK {
			t.Fatalf("ctl exec %s failed: %+v", id, resp.Error)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st, err := sup.Open(ctx, link.KindExec, ExecHeader{ID: id})
		if err != nil {
			t.Fatalf("open exec stream %s: %v", id, err)
		}
		defer st.Close()
		if stdinData != "" {
			if _, err := st.Write([]byte(stdinData)); err != nil {
				t.Fatalf("write stdin: %v", err)
			}
		}
		if err := st.CloseWrite(); err != nil {
			t.Fatalf("close write: %v", err)
		}
		var out, errOut bytes.Buffer
		code, err := api.ReadExecStream(st, &out, &errOut)
		if err != nil {
			t.Fatalf("ReadExecStream %s: %v", id, err)
		}
		return code, out.String(), errOut.String()
	}

	// 1. Separate stdout & stderr + stdin EOF via CloseWrite.
	code, out, errOut := runExec("e1", []string{"/bin/sh", "-c", "read line; echo OUT:$line; echo ERR:oops >&2"}, 5000, "hello\n")
	if code != 0 || strings.TrimSpace(out) != "OUT:hello" || strings.TrimSpace(errOut) != "ERR:oops" {
		t.Fatalf("e1 = (%d, %q, %q)", code, out, errOut)
	}

	// 2. Non-zero exit code (`false` -> 1).
	code, _, _ = runExec("e2", []string{"/bin/sh", "-c", "exit 1"}, 5000, "")
	if code != 1 {
		t.Fatalf("e2 exit = %d, want 1", code)
	}

	// 3. Command not found -> 127.
	code, _, _ = runExec("e3", []string{"no-such-command-xyz"}, 5000, "")
	if code != 127 {
		t.Fatalf("e3 exit = %d, want 127", code)
	}

	// 4. Non-executable file -> 126.
	nonExec := filepath.Join(tmp, "notexec.sh")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = runExec("e4", []string{nonExec}, 5000, "")
	if code != 126 {
		t.Fatalf("e4 exit = %d, want 126", code)
	}

	// 5. Timeout -> 124 (kills running command).
	code, _, _ = runExec("e5", []string{"/bin/sh", "-c", "sleep 30"}, 150, "")
	if code != 124 {
		t.Fatalf("e5 timeout exit = %d, want 124", code)
	}

	// 6. Client close kills silent command immediately after CloseWrite (stdin EOF).
	marker := filepath.Join(tmp, "survived")
	resp := ctlReq(t, ctlR, ctlW, 10, "exec", ExecReq{
		ID:        "e6",
		Argv:      []string{"/bin/sh", "-c", "echo started; sleep 10; touch " + marker},
		Cwd:       tmp,
		TimeoutMS: 20000,
	})
	if !resp.OK {
		t.Fatalf("ctl exec e6 failed: %+v", resp.Error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sup.Open(ctx, link.KindExec, ExecHeader{ID: "e6"})
	if err != nil {
		t.Fatal(err)
	}
	// Half-close stdin; command must stay alive and emit "started".
	_ = st.CloseWrite()
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(st, hdr); err != nil {
		t.Fatalf("read started hdr: %v", err)
	}
	// Discard started payload, then close the stream both ways.
	buf := make([]byte, int(hdr[1])|int(hdr[2])<<8|int(hdr[3])<<16|int(hdr[4])<<24)
	if _, err := io.ReadFull(st, buf); err != nil {
		t.Fatalf("read started payload: %v", err)
	}
	_ = st.Close()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command survived client close")
	}
}

func TestGuestKeepFilesSaveAndRestore(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	runDir := filepath.Join(tmp, "run")
	keepDir := filepath.Join(runDir, "keep")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keepDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	// Pre-populate kept .bash_history in <runDir>/keep and a symlink attack.
	if err := os.WriteFile(filepath.Join(keepDir, ".bash_history"), []byte("echo first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keepDir, ".bashrc"), []byte("echo evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &runner{
		runDir:    runDir,
		keepFiles: []string{".bash_history", ".bashrc"},
	}
	r.restoreKeepFiles()

	got, err := os.ReadFile(filepath.Join(home, ".bash_history"))
	if err != nil || string(got) != "echo first\n" {
		t.Fatalf("restored .bash_history = %q, %v", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
		t.Fatal(".bashrc was restored despite refusal list")
	}

	// Modify .bash_history in guest home, save on stop, verify persistence.
	if err := os.WriteFile(filepath.Join(home, ".bash_history"), []byte("echo first\necho second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.saveKeepFiles()
	saved, err := os.ReadFile(filepath.Join(keepDir, ".bash_history"))
	if err != nil || string(saved) != "echo first\necho second\n" {
		t.Fatalf("saved .bash_history = %q, %v", string(saved), err)
	}
}

func TestPrepareSessionEnvSharesCachesAndBrowserWithExecAndChildren(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	runDir := filepath.Join(tmp, "run")
	agentsBin := filepath.Join(tmp, "agents-bin")
	for _, dir := range []string{home, runDir, agentsBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	r := &runner{
		runDir: runDir,
		opts:   RunOptions{MicroVM: true, AgentsBinDir: agentsBin},
	}
	wantBrowser := BrowserHelperPath(runDir)
	wantStore := filepath.Join(home, filepath.FromSlash(pnpmStoreCacheRel))

	primaryEnv, perr := r.prepareSessionEnv(StartReq{
		Session:   "prim1111",
		Agent:     "claude",
		Cwd:       "/work/app",
		CacheDirs: []string{".npm/_cacache", pnpmStoreCacheRel},
		Env: []string{
			"HOME=" + home,
			"PATH=/usr/bin:/bin",
			brand.Env("HOOK_SOCK") + "=/run/stale-hook.sock",
			brand.Env("SESSION") + "=prim1111",
		},
	}, true)
	if perr != nil {
		t.Fatalf("prepareSessionEnv(first): %+v", perr)
	}

	checkShared := func(label string, env []string) {
		t.Helper()
		m := parseEnvSlice(env)
		if m["BROWSER"] != wantBrowser {
			t.Errorf("%s: BROWSER = %q, want %q", label, m["BROWSER"], wantBrowser)
		}
		if m["pnpm_config_store_dir"] != wantStore {
			t.Errorf("%s: pnpm_config_store_dir = %q, want %q", label, m["pnpm_config_store_dir"], wantStore)
		}
		if m["npm_config_store_dir"] != wantStore {
			t.Errorf("%s: npm_config_store_dir = %q, want %q", label, m["npm_config_store_dir"], wantStore)
		}
		if !strings.HasPrefix(m["PATH"], agentsBin+":") {
			t.Errorf("%s: PATH = %q, want prefix %q", label, m["PATH"], agentsBin+":")
		}
	}

	checkShared("primary", primaryEnv)

	execEnv := r.execBaseEnv([]string{"EXTRA_EXEC=1"})
	checkShared("exec", execEnv)
	execMap := parseEnvSlice(execEnv)
	if execMap["EXTRA_EXEC"] != "1" {
		t.Errorf("exec missing EXTRA_EXEC: %v", execMap)
	}
	if _, has := execMap[brand.Env("HOOK_SOCK")]; has {
		t.Error("exec unexpectedly inherited HOOK_SOCK")
	}
	if _, has := execMap[brand.Env("SESSION")]; has {
		t.Error("exec unexpectedly inherited SESSION")
	}

	childEnv, perr := r.prepareSessionEnv(StartReq{
		Session: "chld2222",
		Agent:   "shell",
		Cwd:     "/work/app",
		Env:     []string{"TERM=xterm-256color", "BROWSER=/bin/spoofed"},
	}, false)
	if perr != nil {
		t.Fatalf("prepareSessionEnv(child): %+v", perr)
	}
	checkShared("child", childEnv)
	childMap := parseEnvSlice(childEnv)
	if _, has := childMap[brand.Env("HOOK_SOCK")]; has {
		t.Error("child unexpectedly inherited primary HOOK_SOCK")
	}
	if _, has := childMap[brand.Env("SESSION")]; has {
		t.Error("child unexpectedly inherited primary SESSION")
	}
}
