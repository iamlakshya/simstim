package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

func repoReq(dir string, changes bool) gitOpRequest {
	return gitOpRequest{RepoInfoArgs: api.RepoInfoArgs{Dir: dir, Changes: changes}}
}

// fakeBin puts executable scripts on PATH, for the rest of the test.
func fakeBin(t *testing.T, scripts map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		put(t, filepath.Join(dir, name), "#!/bin/sh\n"+body, 0o755)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeSocket makes the user manager's socket exist, or not, under the
// test's XDG_RUNTIME_DIR.
func fakeSocket(t *testing.T, exists bool) {
	t.Helper()
	p := userManagerSocket()
	if !exists {
		_ = os.Remove(p)
		return
	}
	put(t, p, "", 0o600)
}

// realGit is the path of git, before a test puts a fake one on PATH.
func realGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	return p
}

// gitAtLeast reports whether git is version major.minor or later.
func gitAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	out, err := exec.Command("git", "version").Output()
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if err != nil || m == nil {
		t.Fatalf("git version: %q %v", out, err)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}

func logLines(path string) []string {
	b, _ := os.ReadFile(path)
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// Spec 19.3, test 2: every git command line starts with the options that
// turn hooks off, and turns off each hook that git's config names. With a
// systemd user manager, each operation runs once through systemd-run, in a
// scope under the sessions' slice.
func TestGitOpsRunInAScope(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	for _, kv := range [][]string{{"hook.evil.command", "true"}, {"hook.evil.event", "post-checkout"}} {
		if out, err := git(repo, "config", kv[0], kv[1]); err != nil {
			t.Fatalf("git config: %v %s", err, out)
		}
	}
	log := filepath.Join(t.TempDir(), "log")
	fakeSocket(t, true)
	fakeBin(t, map[string]string{
		"git":       "echo \"git $*\" >> " + log + "\nexec " + realGit(t) + " \"$@\"\n",
		"systemctl": "echo running\n",
		"systemd-run": "echo \"systemd-run $* scope=$" + gitScopeEnv + "\" >> " + log + "\n" +
			"while [ $# -gt 0 ]; do case $1 in --*) shift;; *) break;; esac; done\nexec \"$@\"\n",
	})
	gi, err := makeWorktree(repo, "fix", "abcd2345", "")
	if err != nil {
		t.Fatal(err)
	}
	if key, _, err := repoOf(gi.Worktree); key != repo || err != nil {
		t.Errorf("repoOf = %s, %v", key, err)
	}
	if got, _ := removeWorktree(gi); got != "removed the clean worktree and its branch" {
		t.Errorf("remove = %q", got)
	}
	gi, err = makeWorktree(repo, "gone", "efgh2345", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := discardWorktree(gi); err != nil || !missing(gi.Worktree) {
		t.Errorf("discard = %v, and the worktree is still there: %v", err, !missing(gi.Worktree))
	}

	lines := logLines(log)
	scopeRE := regexp.MustCompile(`^systemd-run --user --scope --quiet --collect --slice=` + regexp.QuoteMeta(brand.Slice) +
		` --unit=(` + regexp.QuoteMeta(brand.Name) + `-git-[0-9a-f]{8}\.scope) \S+ git-op (\S+) scope=(\S+)$`)
	safe := "git " + strings.Join(gitSafe, " ") + " "
	var ops []string
	reads, commands, versions := 0, 0, 0
	for i, line := range lines {
		if m := scopeRE.FindStringSubmatch(line); m != nil {
			ops = append(ops, m[2])
			if m[3] != m[1] {
				t.Errorf("the child of %s was told its scope is %s", m[1], m[3])
			}
			continue
		}
		switch {
		case i == 0 || !strings.HasPrefix(line, safe):
			t.Errorf("line %d does not start with the options: %q", i, line)
		case line == safe+"version": // the version gate
			versions++
		case strings.Contains(line, ` config -z --name-only --get-regexp ^hook\..*\.command$`):
			reads++
		case !strings.Contains(line, " -c hook.evil.enabled=false -C "):
			t.Errorf("line %d does not turn off the config's hook: %q", i, line)
		default:
			commands++
		}
	}
	if want := []string{gitOpWorktreeAdd, gitOpRepoInfo, gitOpWorktreeRemove, gitOpWorktreeAdd, gitOpWorktreeDiscard}; !slices.Equal(ops, want) {
		t.Errorf("operations through systemd-run = %q, want %q", ops, want)
	}
	if reads == 0 || reads != commands {
		t.Errorf("%d git commands read the config's hooks first, of %d", reads, commands)
	}
	// Each operation but repo.info without its count fires hooks, and
	// passes the version gate first.
	if versions != 4 {
		t.Errorf("the version gate ran %d times, want 4", versions)
	}
}

// The choice of a scope fails closed: when the user manager's socket exists
// but the manager does not say that it runs, git never runs.
func TestGitScopeFailsClosed(t *testing.T) {
	repo := setupRepo(t)
	defer func(d time.Duration) { gitScopeProbeTimeout = d }(gitScopeProbeTimeout)
	for _, c := range []struct {
		name, systemctl string
		socket, scoped  bool
		fails           string
	}{
		{"no socket", "echo running\n", false, false, ""},
		{"offline", "echo offline; exit 1\n", true, false, ""},
		{"running", "echo running\n", true, true, ""},
		{"degraded", "echo degraded; exit 1\n", true, true, ""},
		{"maintenance", "echo maintenance; exit 1\n", true, false, "it is maintenance, with exit code 1"},
		{"stopping", "echo stopping; exit 1\n", true, false, "it is stopping, with exit code 1"},
		{"no answer", "echo 'Failed to connect to bus' >&2; exit 1\n", true, false, "it gave no state, and exit code 1"},
		{"running, then exit 3", "printf 'running\\n'; exit 3\n", true, false, "it is running, with exit code 3"},
		{"running, with exit 1", "printf 'running\\n'; exit 1\n", true, false, "it is running, with exit code 1"},
		{"too slow", "sleep 30\n", true, false, "did not answer within 1s"},
		{"offline, then too slow", "printf 'offline\\n'; sleep 30\n", true, false, "did not answer within 1s"},
		{"running, then too slow", "printf 'running\\n'; sleep 30\n", true, false, "did not answer within 1s"},
		{"offline, with its output open", "printf 'offline\\n'; sleep 30 & exit 1\n", true, false, "its output did not close after it ended"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A new script can take a second to start on a busy Mac, so
			// only the slow cases get a short wait.
			gitScopeProbeTimeout = 10 * time.Second
			if strings.HasSuffix(c.name, "too slow") {
				gitScopeProbeTimeout = time.Second
			}
			log := filepath.Join(t.TempDir(), "log")
			fakeSocket(t, c.socket)
			fakeBin(t, map[string]string{
				"systemctl":   c.systemctl,
				"git":         "echo git >> " + log + "\nexec " + realGit(t) + " \"$@\"\n",
				"systemd-run": "echo systemd-run >> " + log + "\nwhile [ $# -gt 0 ]; do case $1 in --*) shift;; *) break;; esac; done\nexec \"$@\"\n",
			})
			_, err := gitOp(gitOpRepoInfo, repoReq(repo, false))
			lines := logLines(log)
			if c.fails != "" {
				var pe *proto.Error
				if !errors.As(err, &pe) || pe.Code != api.CodeGitScope || !strings.Contains(pe.Message, c.fails) || pe.Hint == "" {
					t.Errorf("gitOp = %v, want git_scope that says %q", err, c.fails)
				}
				if len(lines) != 0 {
					t.Errorf("git ran although the probe failed: %q", lines)
				}
				return
			}
			if err != nil || len(lines) == 0 || (lines[0] == "systemd-run") != c.scoped {
				t.Errorf("gitOp = %v, ran %q, want a scope: %v", err, lines, c.scoped)
			}
		})
	}
}

// A systemd-run that cannot make the scope fails the operation with
// git_scope: git never runs outside the slice instead. A start that made a
// worktree and cannot remove it says where it is.
func TestGitScopeError(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	fakeSocket(t, true)
	fakeBin(t, map[string]string{
		"systemctl":   "echo running\n",
		"systemd-run": "while [ $# -gt 0 ]; do case $1 in --*) shift;; *) break;; esac; done\nexec \"$@\"\n",
	})
	gi, err := makeWorktree(repo, "kept", "abcd2345", "")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "git.log")
	fakeBin(t, map[string]string{
		"systemd-run": "echo 'Failed to connect to bus: No such file or directory' >&2\nexit 1\n",
		"git":         "echo \"$*\" >> " + log + "\nexit 1\n",
	})
	_, err = gitOp(gitOpRepoInfo, repoReq(repo, false))
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeGitScope || !strings.Contains(pe.Message, "could not start git in a scope on this host: Failed to connect to bus") ||
		!strings.Contains(pe.Hint, brand.Name+" host doctor") {
		t.Fatalf("gitOp = %#v", err)
	}
	if _, _, err := repoOf(repo); !errors.As(err, &pe) || pe.Code != api.CodeGitScope {
		t.Errorf("repoOf = %v, want git_scope", err)
	}
	if _, err := makeWorktree(repo, "x", "efgh2345", ""); !errors.As(err, &pe) || pe.Code != api.CodeGitScope {
		t.Errorf("makeWorktree = %v, want git_scope", err)
	}
	want := fmt.Sprintf("The new worktree %s (branch %s) is still on the host", gi.Worktree, gi.Branch)
	if _, err := setupHooks(NewArgs{}, gi.Worktree, gi); !errors.As(err, &pe) || pe.Code != api.CodeGitScope || !strings.Contains(pe.Message, want) || missing(gi.Worktree) {
		t.Errorf("setupHooks with a new worktree = %v, want git_scope that says %q", err, want)
	}
	if got, kept := removeWorktree(gi); kept == nil || kept.Code != api.CodeGitScope || !strings.Contains(got, "kept the worktree") || !strings.Contains(got, "Failed to connect to bus") {
		t.Errorf("removeWorktree = %q", got)
	}
	if !missing(log) {
		t.Error("git ran without its scope")
	}
}

// Spec 19.3, test 3: with real git, a repository's fsmonitor, its hook
// scripts and the hooks that its config defines do not run when the helper
// makes and removes a worktree. git status runs the clean filter, as the
// spec says: Apple Git 2.50, Debian's 2.47 and Alpine's 2.54 and 2.56 do.
func TestHostGitRunsNoHooks(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	put(t, filepath.Join(repo, "README"), "app\n", 0o644)
	put(t, filepath.Join(repo, ".gitattributes"), "README filter=mark\n", 0o644)
	for _, args := range [][]string{{"add", "README", ".gitattributes"}, {"commit", "-q", "-m", "attributes"}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	marks := t.TempDir()
	mark := func(name, then string) string {
		p := filepath.Join(marks, name+".sh")
		put(t, p, "#!/bin/sh\necho ran >> "+filepath.Join(marks, name)+"\n"+then, 0o755)
		return p
	}
	hooks := []string{"post-index-change", "reference-transaction", "post-checkout"}
	for _, h := range hooks {
		put(t, filepath.Join(repo, ".git", "hooks", h), "#!/bin/sh\nexec "+mark(h, "")+"\n", 0o755)
	}
	config := [][]string{{"core.fsmonitor", mark("fsmonitor", "exit 1\n")}, {"filter.mark.clean", mark("filter", "cat\n")},
		{"hook.evil.command", mark("config-hook", "")}}
	for _, h := range hooks {
		config = append(config, []string{"--add", "hook.evil.event", h})
	}
	for _, kv := range config {
		if out, err := git(repo, append([]string{"config"}, kv...)...); err != nil {
			t.Fatalf("git config %v: %v %s", kv, err, out)
		}
	}

	gi, err := makeWorktree(repo, "hooks", "abcd2345", "")
	if err != nil {
		t.Fatal(err)
	}
	// New stat data and the same content: git status must hash README again.
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(gi.Worktree, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	if got, _ := removeWorktree(gi); got != "removed the clean worktree and its branch" {
		t.Fatalf("remove = %q", got)
	}
	for _, name := range append([]string{"fsmonitor", "config-hook"}, hooks...) {
		if !missing(filepath.Join(marks, name)) {
			t.Errorf("%s ran", name)
		}
	}
	if missing(filepath.Join(marks, "filter")) {
		t.Error("git status did not run the clean filter, as spec 19.3 says it does")
	}

	// The fixture is sound: plain git runs the fsmonitor and every hook,
	// and from git 2.54 the hook that the config defines.
	plain := filepath.Join(t.TempDir(), "plain")
	for _, args := range [][]string{{"-C", repo, "worktree", "add", "-q", "-b", "plain", plain}, {"-C", plain, "status", "--porcelain"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("plain git %v: %v %s", args, err, out)
		}
	}
	for _, name := range append([]string{"fsmonitor"}, hooks...) {
		if missing(filepath.Join(marks, name)) {
			t.Errorf("plain git did not run %s, so the test proves nothing about it", name)
		}
	}
	if ran, want := !missing(filepath.Join(marks, "config-hook")), gitAtLeast(t, 2, 54); ran != want {
		t.Errorf("plain git ran the config's hook: %v, want %v", ran, want)
	}
}

// A hook that git's config names in a way that a -c option cannot carry
// stops git: it runs nothing, and says why.
func TestGitRefusesAHookItCannotTurnOff(t *testing.T) {
	repo := setupRepo(t)
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[hook \"a=b\"]\n\tcommand = true\n")
	f.Close()
	_, err = git(repo, "status")
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeGitHooks || !strings.Contains(pe.Message, `"a=b"`) || pe.Hint == "" {
		t.Errorf("git status = %v, want git_hooks", err)
	}
	if _, err := gitOp(gitOpRepoInfo, repoReq(repo, false)); !errors.As(err, &pe) || pe.Code != api.CodeGitHooks {
		t.Errorf("repo.info = %v, want git_hooks", err)
	}
}

// processGone reports whether the process pid has ended.
func processGone(pid int) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return true
		}
		s := string(b)
		return strings.HasPrefix(strings.TrimSpace(s[strings.LastIndexByte(s, ')')+1:]), "Z")
	}
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// tryDeadlines runs try with each deadline in turn, until try says that the
// filter started before the deadline. A busy machine can take more than 8
// seconds to start the child and the git commands before the filter.
func tryDeadlines(t *testing.T, k *gitOpKind, try func(d time.Duration) (started bool)) {
	t.Helper()
	defer func(d time.Duration) { k.deadline = d }(k.deadline)
	for _, d := range []time.Duration{8 * time.Second, 20 * time.Second, 45 * time.Second} {
		k.deadline = d
		if try(d) {
			return
		}
		t.Logf("the filter did not start within %s, so the test tries a longer deadline", d)
	}
	t.Fatal("the filter never started before the deadline")
}

// filterGone waits until each process in the pids file has ended, and
// kills one that has not.
func filterGone(t *testing.T, pids []string) {
	t.Helper()
	for _, p := range pids {
		pid, _ := strconv.Atoi(p)
		deadline := time.Now().Add(15 * time.Second)
		for !processGone(pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !processGone(pid) {
			t.Errorf("the filter %d still runs", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// A filter that hangs runs into the operation's deadline: the helper stops
// it, and its scope, and says so.
func TestGitOpDeadline(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	marks := t.TempDir()
	put(t, filepath.Join(marks, "slow.sh"), "#!/bin/sh\necho $$ >> "+filepath.Join(marks, "pids")+"\nsleep 60\ncat\n", 0o755)
	put(t, filepath.Join(repo, "README"), "app\n", 0o644)
	put(t, filepath.Join(repo, ".gitattributes"), "README filter=slow\n", 0o644)
	for _, args := range [][]string{{"add", "README", ".gitattributes"}, {"commit", "-q", "-m", "attributes"}, {"config", "filter.slow.clean", filepath.Join(marks, "slow.sh")}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scope %v", scoped), func(t *testing.T) {
			tryDeadlines(t, findGitOp(gitOpRepoInfo), func(d time.Duration) bool {
				log := filepath.Join(t.TempDir(), "log")
				fakeSocket(t, scoped)
				fakeBin(t, map[string]string{
					"systemctl":   "case \"$*\" in *is-system-running*) echo running;; *) echo \"systemctl $*\" >> " + log + ";; esac\n",
					"systemd-run": "while [ $# -gt 0 ]; do case $1 in --*) shift;; *) break;; esac; done\nexec \"$@\"\n",
				})
				_ = os.Remove(filepath.Join(marks, "pids"))
				later := time.Now().Add(10 * time.Second)
				if err := os.Chtimes(filepath.Join(repo, "README"), later, later); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				_, err := gitOp(gitOpRepoInfo, repoReq(repo, true))
				took := time.Since(start)
				pids := logLines(filepath.Join(marks, "pids"))
				if len(pids) == 0 {
					return false
				}
				var pe *proto.Error
				if !errors.As(err, &pe) || pe.Code != api.CodeGitTimeout || !strings.Contains(pe.Message, fmt.Sprintf("longer than %s in repo.info", d)) || pe.Hint == "" {
					t.Errorf("gitOp = %v, want git_timeout", err)
				}
				if took > d+30*time.Second {
					t.Errorf("gitOp took %s", took)
				}
				filterGone(t, pids)
				stops := logLines(log)
				if scoped && (len(stops) != 1 || !regexp.MustCompile(`^systemctl --user stop `+regexp.QuoteMeta(brand.Name)+`-git-[0-9a-f]{8}\.scope$`).MatchString(stops[0])) {
					t.Errorf("systemctl calls = %q, want one stop of the scope", stops)
				}
				if !scoped && len(stops) != 0 {
					t.Errorf("systemctl calls without a scope: %q", stops)
				}
				return true
			})
		})
	}
}

// Spec 19.3, test 6: repo.info in two worktrees of one repository names the
// same main folder, so both use one setup-trust answer.
func TestRepoInfoInTwoWorktrees(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	one, err := makeWorktree(repo, "one", "abcd2345", "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := makeWorktree(repo, "two", "efgh2345", "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(one.Worktree, "new.txt"), "work\n", 0o644)
	for _, g := range []*GitInfo{one, two} {
		ans, err := gitOp(gitOpRepoInfo, repoReq(g.Worktree, true))
		if err != nil || ans.Repo == nil {
			t.Fatalf("repo.info in %s: %+v, %v", g.Worktree, ans, err)
		}
		dirty := 0
		if g == one {
			dirty = 1
		}
		if want := (api.RepoInfo{Root: g.Worktree, Main: repo, Branch: g.Branch, Dirty: dirty}); *ans.Repo != want {
			t.Errorf("repo.info = %+v, want %+v", *ans.Repo, want)
		}
		if key, top, err := repoOf(g.Worktree); key != repo || top != g.Worktree || err != nil {
			t.Errorf("repoOf(%s) = %s, %s, %v", g.Worktree, key, top, err)
		}
	}
	// Without Changes, repo.info runs no git status, and counts nothing.
	if ans, err := gitOp(gitOpRepoInfo, repoReq(one.Worktree, false)); err != nil || ans.Repo.Dirty != 0 {
		t.Errorf("repo.info without changes = %+v, %v", ans.Repo, err)
	}
	// Outside a repository, repo.info says so, and repoOf keys by the folder.
	plain := t.TempDir()
	var pe *proto.Error
	if _, err := gitOp(gitOpRepoInfo, repoReq(plain, true)); !errors.As(err, &pe) || pe.Code != "not_a_repo" || pe.Hint == "" {
		t.Errorf("repo.info outside git = %v", err)
	}
	if key, top, err := repoOf(plain); key != plain || top != plain || err != nil {
		t.Errorf("repoOf(plain) = %s, %s, %v", key, top, err)
	}
	// If git cannot check a worktree's branch, removeWorktreeHere keeps it,
	// and says why with a code and a hint.
	got, err := removeWorktreeHere(&GitInfo{RepoRoot: repo, Worktree: two.Worktree, Branch: "gone", BaseSHA: two.BaseSHA, CreatedBy: true})
	if !errors.As(err, &pe) || pe.Code != "worktree" || pe.Hint == "" || got != "" || missing(two.Worktree) {
		t.Errorf("removeWorktreeHere with a missing branch = %q, %v", got, err)
	}
}

// The git-op command: its child runs the operation itself, and any other
// git-op starts the child first. A bad call says how to make a good one.
func TestRunGitOp(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	req := `{"dir":"` + repo + `","changes":true}`
	for _, scope := range []string{"", gitNoScope} {
		t.Setenv(gitScopeEnv, scope)
		var out bytes.Buffer
		if err := RunGitOp([]string{gitOpRepoInfo}, strings.NewReader(req), &out); err != nil {
			t.Fatal(err)
		}
		var ans gitOpAnswer
		if err := json.Unmarshal(out.Bytes(), &ans); err != nil || ans.Repo == nil || ans.Repo.Main != repo || ans.Repo.Branch != "main" {
			t.Errorf("%s=%q: answer %s, %v", gitScopeEnv, scope, out.Bytes(), err)
		}
	}
	var out bytes.Buffer
	if err := RunGitOp([]string{gitOpRepoInfo}, strings.NewReader(`{"dir":"`+t.TempDir()+`"}`), &out); err != nil || !strings.Contains(out.String(), `"code":"not_a_repo"`) {
		t.Errorf("repo.info outside git = %s, %v", out.Bytes(), err)
	}
	var pe *proto.Error
	for _, c := range []struct {
		args []string
		in   string
		code string
	}{
		{nil, req, "usage"}, {[]string{"status"}, req, "usage"}, {[]string{gitOpRepoInfo}, "not json", "bad_request"},
	} {
		if err := RunGitOp(c.args, strings.NewReader(c.in), &out); !errors.As(err, &pe) || pe.Code != c.code || pe.Hint == "" {
			t.Errorf("git-op %q: %v, want %s with a hint", c.args, err, c.code)
		}
	}
	if err := RunGitOp(nil, nil, &out); err == nil || !strings.Contains(err.Error(), "<worktree.add|worktree.remove|worktree.discard|repo.info>") {
		t.Errorf("usage = %v", err)
	}
}

// Every error of a git operation that reaches the user has a hint.
func TestGitOpErrorsHaveHints(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no user manager
	var pe *proto.Error
	if _, err := gitOp("no.such.op", gitOpRequest{}); !errors.As(err, &pe) || pe.Hint == "" {
		t.Errorf("an unknown operation: %v", err)
	}
	if e := asProtoError(errors.New("plain")); e.Code != "git" || e.Hint == "" {
		t.Errorf("asProtoError = %+v", e)
	}
	defer SetExecutable("/no/such/helper")()
	if _, err := gitOp(gitOpRepoInfo, repoReq(t.TempDir(), false)); !errors.As(err, &pe) || pe.Code != "git_op" || pe.Hint == "" {
		t.Errorf("a helper that does not start: %v", err)
	}
}

func TestGitOpArgv(t *testing.T) {
	if scope, argv := gitOpArgv("/bin/h", gitOpRepoInfo, false); scope != gitNoScope || !slices.Equal(argv, []string{"/bin/h", "git-op", gitOpRepoInfo}) {
		t.Errorf("without systemd: %s %q", scope, argv)
	}
	a, _ := gitOpArgv("/bin/h", gitOpRepoInfo, true)
	b, argv := gitOpArgv("/bin/h", gitOpRepoInfo, true)
	if a == b || !slices.Contains(argv, "--unit="+b) || !slices.Contains(argv, "--slice="+brand.Slice) || argv[0] != "systemd-run" {
		t.Errorf("with systemd: %s %s %q", a, b, argv)
	}
	got := gitArgs("/r", []string{"evil"}, "status")
	if !slices.Equal(got[:4], []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}) ||
		!slices.Equal(got[len(got)-5:], []string{"-c", "hook.evil.enabled=false", "-C", "/r", "status"}) {
		t.Errorf("gitArgs = %q", got)
	}
	for _, e := range []string{"post-checkout", "post-index-change", "reference-transaction", "pre-auto-gc", "fsmonitor-watchman"} {
		if !slices.Contains(got, "hook."+e+".enabled=false") {
			t.Errorf("gitArgs does not turn off %s", e)
		}
	}
	if len(gitHookEvents) != 28 {
		t.Errorf("githooks(5) lists 28 events, and the helper %d", len(gitHookEvents))
	}
}

// The host doctor's line about git's scope: none without a user manager,
// and otherwise whether systemd-run can make a scope under the slice.
func TestGitScopeCheck(t *testing.T) {
	setupRepo(t)
	defer func(d time.Duration) { gitScopeProbeTimeout = d }(gitScopeProbeTimeout)
	gitScopeProbeTimeout = 10 * time.Second
	fakeSocket(t, false)
	if c := gitScopeCheck(); c != nil {
		t.Errorf("without a user manager: %+v", c)
	}
	log := filepath.Join(t.TempDir(), "log")
	fakeSocket(t, true)
	for _, c := range []struct{ systemctl, systemdRun, want string }{
		{"echo running\n", "echo \"$*\" >> " + log + "\n", "✓ the helper's git runs in a scope under " + brand.Slice},
		{"echo running\n", "echo 'Failed to start transient scope unit: Access denied' >&2\nexit 1\n", "✕ systemd-run cannot make a scope under " + brand.Slice + ", so no git step can run: Failed to start transient scope unit: Access denied"},
		{"echo maintenance; exit 1\n", "exit 0\n", "✕ could not start git in a scope on this host: could not ask its systemd user manager whether it runs: it is maintenance, with exit code 1"},
	} {
		fakeBin(t, map[string]string{"systemctl": c.systemctl, "systemd-run": c.systemdRun})
		got := gitScopeCheck()
		if len(got) != 1 {
			t.Fatalf("checks = %+v", got)
		}
		line := map[bool]string{true: "✓ ", false: "✕ "}[got[0].OK] + got[0].Detail
		if line != c.want || got[0].Name != "git scope" || (!got[0].OK && got[0].Fix == "") {
			t.Errorf("check = %+v, want %q", got[0], c.want)
		}
	}
	if b, _ := os.ReadFile(log); !regexp.MustCompile(`^--user --scope --quiet --collect --slice=` + regexp.QuoteMeta(brand.Slice) + ` --unit=\S+\.scope true\n$`).Match(b) {
		t.Errorf("systemd-run got %q", b)
	}
	// The host doctor shows the line. It runs no agent of the developer's.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	fakeBin(t, map[string]string{"systemctl": "echo running\n", "systemd-run": "exit 0\n", "loginctl": "echo yes\n"})
	if !slices.ContainsFunc(Doctor().Checks, func(c Check) bool { return c.Name == "git scope" && c.OK }) {
		t.Error("the host doctor has no git scope line")
	}
}
