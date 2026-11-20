package host

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// setupRepo makes a git repository with a committed, executable setup
// script, and returns its folder.
func setupRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	alertHome(t)
	data, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("XDG_DATA_HOME", data)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": os.DevNull} {
		t.Setenv(k, v)
	}
	childRaces(t)
	// No systemd user manager can be reached from here, as in hosttest.New,
	// so git never runs in the developer's own slice. The scope tests fake
	// one.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	// A fake systemctl can take seconds to start on a busy machine.
	old := gitScopeProbeTimeout
	gitScopeProbeTimeout = 30 * time.Second
	t.Cleanup(func() { gitScopeProbeTimeout = old })
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "app")
	hook := filepath.Join(repo, "."+brand.Name, "setup")
	_ = os.MkdirAll(filepath.Dir(hook), 0o755)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho setting up\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

// childRaces makes the race detector of the git operations' children, which
// are this test binary, write its reports to a folder, and fails the test
// when one does. It also stops their pause at exit, a second each.
func childRaces(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GORACE", "log_path="+filepath.Join(dir, "race")+" atexit_sleep_ms=0")
	t.Cleanup(func() {
		reports, _ := filepath.Glob(filepath.Join(dir, "race.*"))
		for _, r := range reports {
			b, _ := os.ReadFile(r)
			t.Errorf("a git operation's child found a data race:\n%s", b)
		}
	})
}

func question(t *testing.T, err error) api.SetupQuestion {
	t.Helper()
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeSetupTrust {
		t.Fatalf("want a setup_trust question, got %v", err)
	}
	var q api.SetupQuestion
	if json.Unmarshal(pe.Data, &q) != nil {
		t.Fatalf("data %s", pe.Data)
	}
	return q
}

// hashOf is the setup hash of the scripts at top, for a test.
func hashOf(t *testing.T, top string) string {
	t.Helper()
	s, err := setupHash(top)
	if err != nil {
		t.Fatal(err)
	}
	return s.hash
}

func TestSetupHooksNeedTrust(t *testing.T) {
	repo := setupRepo(t)
	script := filepath.Join(repo, "."+brand.Name, "setup")
	sub := filepath.Join(repo, "src")
	_ = os.MkdirAll(sub, 0o755)
	hash := hashOf(t, repo)

	// Unknown: ask, from the repository's root, even in a subfolder. The
	// question carries the setup hash.
	_, err := setupHooks(NewArgs{}, sub, nil)
	if q := question(t, err); q.Repo != repo || q.Script != script || q.Hash != hash {
		t.Fatalf("question %+v, want hash %s", q, hash)
	}
	// The order of spec 19.2: the override, the refusal, the hash.
	for _, c := range []struct {
		name string
		a    NewArgs
		want string
	}{
		{"--setup", NewArgs{Setup: api.SetupRun, SetupTrust: map[string]bool{repo: false}}, script},
		{"--no-setup", NewArgs{Setup: api.SetupSkip, SetupHashes: map[string]string{repo: hash}}, ""},
		{"refused", NewArgs{SetupTrust: map[string]bool{repo: false}, SetupHashes: map[string]string{repo: hash}}, ""},
		{"trusted", NewArgs{SetupHashes: map[string]string{repo: hash}}, script},
	} {
		h, err := setupHooks(c.a, sub, nil)
		if err != nil || h.setup != c.want {
			t.Errorf("%s: setup %q, %v; want %q", c.name, h.setup, err, c.want)
		}
		if (h.hash != "") != (c.want != "") || h.hash != "" && h.hash != hash {
			t.Errorf("%s: hash %q, want it only when the scripts run", c.name, h.hash)
		}
	}
	// Otherwise it asks: for another hash, saying that the script changed,
	// and for an older client's yes without a hash.
	_, err = setupHooks(NewArgs{SetupHashes: map[string]string{repo: "an older hash"}}, sub, nil)
	var pe *proto.Error
	if q := question(t, err); q.Hash != hash || !errors.As(err, &pe) || !strings.Contains(pe.Message, "changed after you trusted it") {
		t.Errorf("changed: %v, question %+v", err, q)
	}
	if q := question(t, func() error {
		_, err := setupHooks(NewArgs{SetupTrust: map[string]bool{repo: true}}, sub, nil)
		return err
	}()); q.Hash != hash {
		t.Errorf("an older client's yes: question %+v", q)
	}
	// A refused repository falls back to the host's own hook.
	own := filepath.Join(paths.ConfigDir(), "hooks", "setup")
	_ = os.MkdirAll(filepath.Dir(own), 0o755)
	_ = os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755)
	if h, _ := setupHooks(NewArgs{Setup: api.SetupSkip}, repo, nil); h.setup != own {
		t.Errorf("refused repository: setup %q, want the host's %q", h.setup, own)
	}
	// A folder without hooks asks nothing.
	if h, err := setupHooks(NewArgs{}, t.TempDir(), nil); err != nil || h.setup != own {
		t.Errorf("plain folder: %+v %v", h, err)
	}
}

func TestSetupHooksInANewWorktree(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	gi, err := makeWorktree(repo, "fix", "abcd2345", "")
	if err != nil {
		t.Fatal(err)
	}
	// The worktree shares the repository's answer.
	if key, top, err := repoOf(gi.Worktree); key != repo || top != gi.Worktree || err != nil {
		t.Fatalf("repoOf(worktree) = %s, %s, %v", key, top, err)
	}
	// The worktree's scripts are the repository's: the same hash.
	h, err := setupHooks(NewArgs{SetupHashes: map[string]string{repo: hashOf(t, repo)}}, gi.Worktree, gi)
	if err != nil || h.setup != filepath.Join(gi.Worktree, "."+brand.Name, "setup") {
		t.Fatalf("trusted worktree: %+v %v", h, err)
	}
	// Asking about a worktree removes the one this start made, and its branch.
	question(t, func() error { _, err := setupHooks(NewArgs{}, gi.Worktree, gi); return err }())
	if _, err := os.Stat(gi.Worktree); !os.IsNotExist(err) {
		t.Error("the worktree is still there")
	}
	if out, _ := git(repo, "branch", "--list", gi.Branch); out != "" {
		t.Errorf("the branch is still there: %s", out)
	}
}

func TestSetupScriptRunsBeforeTheAgent(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "setup")
	body := "#!/bin/sh\necho \"hook $" + brand.Env("HOOK") + " new=$" + brand.Env("WORKTREE_NEW") + " name=$" + brand.Env("SESSION_NAME") + " dir=$" + brand.Env("SESSION_DIR") + "\"\nexit 3\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Name: "my task", Cwd: "/work/app wt", Git: &GitInfo{CreatedBy: true, RepoRoot: "/work/app"}}
	argv := startArgv([]string{"echo", "AGENT"}, hook, hookVars(m, false), nil)
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "[" + brand.Name + ": running " + hook + "]\n" +
		"hook setup new=1 name=my task dir=/work/app wt\n" +
		"\n[" + brand.Name + ": " + hook + " exited with 3; the agent starts anyway]\n\n" +
		"AGENT\n"
	if string(out) != want {
		t.Fatalf("got\n%q\nwant\n%q", out, want)
	}
	// After a restart the resume hook says so, and the worktree is not new.
	if v := strings.Join(hookVars(m, true), " "); !strings.Contains(v, brand.Env("HOOK")+"=resume") || !strings.Contains(v, brand.Env("WORKTREE_NEW")+"=0") {
		t.Fatalf("resume vars %s", v)
	}
}
