package host

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/ring"
)

const secretValue = "fake-secret-for-tests-0123456789"

// secretHome gives a test its own state folder, and a runtime folder short
// enough for a socket path on macOS.
func secretHome(t *testing.T) {
	t.Helper()
	alertHome(t)
	rt, err := os.MkdirTemp("/tmp", "sec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
}

// secretHolder makes a holder for a session that asks for OPENAI_API_KEY and
// runs argv, with its socket open.
func secretHolder(t *testing.T, argv ...string) *Holder {
	t.Helper()
	id := NewID()
	m := &Manifest{Schema: 1, ID: id, Name: "s", Agent: AgentInfo{Kind: "custom", Argv: argv},
		Cwd: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME")},
		Term: TermInfo{Cols: 80, Rows: 24}, Secrets: []string{"OPENAI_API_KEY"}}
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	lf, err := os.OpenFile(paths.HolderLog(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lf.Close() })
	h := &Holder{id: id, m: m, log: newHolderLog(lf, nil), ring: ring.New(1 << 16), clients: map[*hclient]struct{}{}}
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.ln.Close() })
	return h
}

func TestSecretsReachOnlyTheAgent(t *testing.T) {
	secretHome(t)
	out := filepath.Join(t.TempDir(), "env.txt")
	h := secretHolder(t, "/bin/sh", "-c", "env > "+out+"; exec sleep 30")
	errc := make(chan error, 1)
	go func() {
		// A probe that connects and leaves, as waitSocket does, comes first.
		if c, err := net.Dial("unix", paths.Socket(h.id)); err == nil {
			c.Close()
		}
		errc <- deliverSecrets(h.id, map[string]api.Secret{"OPENAI_API_KEY": secretValue})
	}()
	h.secretsBeforeStart(false)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if err := h.start(); err != nil {
		t.Fatal(err)
	}
	pid := h.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL); h.ptmx.Close() })

	// The agent has the value in its environment.
	deadline := time.Now().Add(10 * time.Second)
	var env []byte
	for time.Now().Before(deadline) {
		if env, _ = os.ReadFile(out); strings.Contains(string(env), "\nOPENAI_API_KEY="+secretValue+"\n") || strings.HasPrefix(string(env), "OPENAI_API_KEY="+secretValue+"\n") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(env), "OPENAI_API_KEY="+secretValue) {
		t.Fatalf("the agent's environment has no secret:\n%s", env)
	}
	if h.m.MissingSecrets != nil || h.secretEnv != nil || h.cmd.Env != nil {
		t.Fatalf("missing %v; the holder kept %d entries, the command %d", h.m.MissingSecrets, len(h.secretEnv), len(h.cmd.Env))
	}

	// Nowhere else: not the session's folder (manifest, holder log), not
	// the session row (`ls --json`), not the agent's command line.
	if err := h.m.Save(); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Walk(paths.SessionDir(h.id), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secretValue) {
				t.Errorf("%s holds the secret", p)
			}
		}
		return nil
	})
	row, _ := json.Marshal([]SessionInfo{infoFrom(h.m, true)})
	if strings.Contains(string(row), secretValue) {
		t.Errorf("the session row holds the secret: %s", row)
	}
	manifest, _ := os.ReadFile(paths.Manifest(h.id))
	if !strings.Contains(string(manifest), `"secrets": [`) || !strings.Contains(string(manifest), `"OPENAI_API_KEY"`) {
		t.Errorf("the manifest does not name the secret:\n%s", manifest)
	}
	args, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || strings.Contains(string(args), secretValue) || !strings.Contains(string(args), "sleep") {
		t.Errorf("the agent's command line %q (%v)", args, err)
	}
}

func TestSecretsMissingAfterARestart(t *testing.T) {
	secretHome(t)
	h := secretHolder(t, "/bin/true")
	start := time.Now()
	h.secretsBeforeStart(true)
	if time.Since(start) > time.Second {
		t.Fatal("a restored session waited for secrets nobody can send")
	}
	if strings.Join(h.m.MissingSecrets, ",") != "OPENAI_API_KEY" || len(h.notes) != 1 || !strings.Contains(h.notes[0], "after a host restart") {
		t.Fatalf("missing %v, notes %q", h.m.MissingSecrets, h.notes)
	}
	if row := infoFrom(h.m, true); strings.Join(row.MissingSecrets, ",") != "OPENAI_API_KEY" {
		t.Fatalf("the session row does not list the missing secret: %+v", row)
	}
	argv := startArgv([]string{"claude"}, "", nil, h.notes)
	if len(argv) != 5 || argv[0] != "/bin/sh" || !strings.Contains(argv[2], "OPENAI_API_KEY") || argv[4] != "claude" {
		t.Fatalf("the note does not show before the agent: %q", argv)
	}
	out, err := exec.Command(argv[0], append(argv[1:4], "echo", "AGENT")...).Output()
	if err != nil || !strings.HasPrefix(string(out), "[") || !strings.HasSuffix(string(out), "\n\nAGENT\n") {
		t.Fatalf("the wrapper printed %q (%v)", out, err)
	}
}

func TestSecretsWaitRefusesOthers(t *testing.T) {
	secretHome(t)
	h := secretHolder(t, "/bin/true")
	done := make(chan []string, 1)
	go func() { done <- h.awaitSecrets([]string{"OPENAI_API_KEY"}, 300*time.Millisecond) }()
	if _, err := HolderRequest(h.id, "peek", nil, time.Second); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("a request while the session waits: %v", err)
	}
	if env := <-done; env != nil {
		t.Fatalf("got %d secrets from nobody", len(env))
	}
}

func TestCheckSecretsNeverEchoesAValue(t *testing.T) {
	for name, val := range map[string]api.Secret{"BAD NAME": secretValue, "TERM": secretValue, "OK": api.Secret(secretValue + "\x00")} {
		_, err := checkSecrets(map[string]api.Secret{name: val})
		if err == nil {
			t.Fatalf("%q was accepted", name)
		}
		if b, _ := json.Marshal(err); strings.Contains(err.Error()+string(b), secretValue) {
			t.Fatalf("the error shows the value: %v", err)
		}
	}
	names, err := checkSecrets(map[string]api.Secret{"B_KEY": "1", "A_KEY": "2"})
	if err != nil || strings.Join(names, ",") != "A_KEY,B_KEY" {
		t.Fatalf("names %v, %v", names, err)
	}
}
