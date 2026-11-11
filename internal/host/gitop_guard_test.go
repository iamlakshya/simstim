package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// git254 reports whether this machine's git is 2.54.x, which the helper
// refuses for a step that fires hooks.
func git254() bool {
	out, _ := exec.Command("git", "version").Output()
	return regexp.MustCompile(`^git version 2\.54(\.|\s|$)`).Match(out)
}

// skipOnGit254 skips a test that runs a step that fires hooks, on git 2.54.
// TestGit254IsRefused covers that git.
func skipOnGit254(t *testing.T) {
	t.Helper()
	if git254() {
		t.Skip("the helper refuses git 2.54 for a step that fires hooks")
	}
}

// fakeGitVersion puts a git on PATH that says it is version, and runs the
// real git for anything else.
func fakeGitVersion(t *testing.T, version string) {
	t.Helper()
	fakeBin(t, map[string]string{"git": "for a; do last=$a; done\n" +
		"if [ \"$last\" = version ]; then echo '" + version + "'; exit 0; fi\nexec " + realGit(t) + " \"$@\"\n"})
}

// Git 2.54 runs the hooks that a repository's config defines, and has no
// switch that turns them all off, so the helper refuses it for each step
// that fires hooks. Before 2.54 and from 2.55, it runs. A step that fires
// no hook, such as repo.info without its count, runs on 2.54 too.
func TestGitVersionGate(t *testing.T) {
	repo := setupRepo(t)
	for _, c := range []struct {
		version string
		refused string
	}{
		{"git version 2.54.1", "git 2.54.1 on this host runs the hooks that a repository's config defines"},
		{"git version 2.54.0.rc1", "git 2.54.0 on this host runs the hooks"},
		{"git version 2.53.0", ""},
		{"git version 2.55.0", ""},
		{"git version 2.50.1 (Apple Git-155)", ""},
		{"not git at all", "could not read the version of git on this host"},
	} {
		t.Run(c.version, func(t *testing.T) {
			fakeGitVersion(t, c.version)
			if _, err := gitOp(gitOpRepoInfo, repoReq(repo, false)); err != nil {
				t.Errorf("repo.info without its count = %v", err)
			}
			_, err := gitOp(gitOpRepoInfo, repoReq(repo, true))
			gi, addErr := makeWorktree(repo, "gate", "abcd2345", "")
			var pe *proto.Error
			if c.refused == "" {
				if err != nil || addErr != nil {
					t.Fatalf("repo.info = %v, worktree.add = %v", err, addErr)
				}
				if err := discardWorktree(gi); err != nil {
					t.Error(err)
				}
				return
			}
			for _, e := range []error{err, addErr} {
				if !errors.As(e, &pe) || pe.Code != api.CodeGitVersion || !strings.Contains(pe.Message, c.refused) || !strings.Contains(pe.Hint, "2.55 or later") {
					t.Errorf("got %v, want git_version that says %q", e, c.refused)
				}
			}
			if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+brand.Name+"/gate-abcd2345"); gi != nil || err == nil || !missing(filepath.Join(WorktreesDir(), "app", "gate-abcd2345")) {
				t.Error("a refused worktree.add made a worktree or a branch")
			}
		})
	}
}

// On a real git 2.54, the helper makes no worktree, and so runs none of the
// repository's hooks.
func TestGit254IsRefused(t *testing.T) {
	if !git254() {
		t.Skip("this machine's git is not 2.54")
	}
	repo := setupRepo(t)
	marks := t.TempDir()
	hook := filepath.Join(marks, "hook.sh")
	put(t, hook, "#!/bin/sh\necho ran >> "+filepath.Join(marks, "ran")+"\n", 0o755)
	for _, kv := range [][]string{{"hook.evil.command", hook}, {"--add", "hook.evil.event", "post-checkout"}, {"--add", "hook.evil.event", "reference-transaction"}} {
		if out, err := git(repo, append([]string{"config"}, kv...)...); err != nil {
			t.Fatalf("git config %v: %v %s", kv, err, out)
		}
	}
	_, err := makeWorktree(repo, "refused", "abcd2345", "")
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeGitVersion {
		t.Errorf("makeWorktree on git 2.54 = %v, want git_version", err)
	}
	if !missing(filepath.Join(marks, "ran")) {
		t.Error("the config's hook ran")
	}
}

// A worktree.add whose checkout runs past its deadline, here in a smudge
// filter that sleeps, is stopped. The helper then removes the worktree and
// the branch that the add had begun. When it cannot, the error says where
// they stay.
func TestWorktreeAddRollsBackAtItsDeadline(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	marks := t.TempDir()
	pidsFile := filepath.Join(marks, "pids")
	put(t, filepath.Join(marks, "slow.sh"), "#!/bin/sh\necho $$ >> "+pidsFile+"\nsleep 60\ncat\n", 0o755)
	put(t, filepath.Join(repo, "README"), "app\n", 0o644)
	put(t, filepath.Join(repo, ".gitattributes"), "README filter=slow\n", 0o644)
	for _, args := range [][]string{{"add", "README", ".gitattributes"}, {"commit", "-q", "-m", "attributes"}, {"config", "filter.slow.smudge", filepath.Join(marks, "slow.sh")}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	branchGone := func(b string) bool {
		_, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
		return err != nil
	}
	attempt := 0
	// start runs worktree.add, and reports whether the smudge filter
	// started, which shows that the add had begun its checkout.
	start := func(name string) (wt, branch string, err error, started bool) {
		attempt++
		id := fmt.Sprintf("abcd234%d", attempt)
		_ = os.Remove(pidsFile)
		_, err = makeWorktree(repo, name, id, "")
		pids := logLines(pidsFile)
		filterGone(t, pids)
		return filepath.Join(WorktreesDir(), "app", name+"-"+id), brand.Name + "/" + name + "-" + id, err, len(pids) > 0
	}

	t.Run("removed", func(t *testing.T) {
		tryDeadlines(t, findGitOp(gitOpWorktreeAdd), func(time.Duration) bool {
			wt, branch, err, started := start("slow")
			if !started {
				return false
			}
			var pe *proto.Error
			if !errors.As(err, &pe) || pe.Code != api.CodeGitTimeout || strings.Contains(pe.Message, "still on the host") {
				t.Errorf("makeWorktree = %v, want git_timeout", err)
			}
			if !missing(wt) || !branchGone(branch) {
				t.Errorf("the stopped add left its worktree or its branch: %v %v", !missing(wt), !branchGone(branch))
			}
			return true
		})
	})

	t.Run("kept", func(t *testing.T) {
		fakeSocket(t, true)
		tryDeadlines(t, findGitOp(gitOpWorktreeAdd), func(time.Duration) bool {
			// systemd-run makes the add's scope, and fails for the removal.
			count := filepath.Join(t.TempDir(), "count")
			fakeBin(t, map[string]string{
				"systemctl": "echo running\n",
				"systemd-run": "echo x >> " + count + "\nif [ $(wc -l < " + count + ") -gt 1 ]; then echo 'Failed to start transient scope unit' >&2; exit 1; fi\n" +
					"while [ $# -gt 0 ]; do case $1 in --*) shift;; *) break;; esac; done\nexec \"$@\"\n",
			})
			wt, branch, err, started := start("kept")
			if !started {
				return false
			}
			var pe *proto.Error
			want := fmt.Sprintf("The new worktree %s (branch %s) is still on the host", wt, branch)
			if !errors.As(err, &pe) || pe.Code != api.CodeGitTimeout || !strings.Contains(pe.Message, want) || !strings.Contains(pe.Message, "worktree remove --force") {
				t.Errorf("makeWorktree = %v, want git_timeout that says %q", err, want)
			}
			return true
		})
	})
}

// A start that asks about a setup script, and cannot remove the worktree
// that it made, still asks, and says where the worktree stays and how to
// remove it.
func TestSetupQuestionSaysWhereAWorktreeStays(t *testing.T) {
	repo := setupRepo(t)
	// Not a worktree, and outside the helper's own folder: the removal
	// fails, and leaves it.
	g := &GitInfo{RepoRoot: repo, Worktree: t.TempDir(), Branch: brand.Name + "/x-abcd2345", CreatedBy: true}
	_, err := setupHooks(NewArgs{}, repo, g)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != api.CodeSetupTrust {
		t.Fatalf("setupHooks = %v, want the setup_trust question", err)
	}
	var q api.SetupQuestion
	if err := json.Unmarshal(pe.Data, &q); err != nil || q.Repo != repo || q.Script == "" {
		t.Fatalf("question %s, %v", pe.Data, err)
	}
	want := fmt.Sprintf("The new worktree %s (branch %s) is still on the host. Remove it there with `%s`", g.Worktree, g.Branch, removeCommand(g))
	if !strings.HasPrefix(q.Kept, want) || !strings.Contains(pe.Message, want) || missing(g.Worktree) {
		t.Errorf("kept = %q, message %q, want %q", q.Kept, pe.Message, want)
	}
	// A removal that works says nothing.
	gi, err := makeWorktree(repo, "asks", "efgh2345", "")
	if err != nil {
		if git254() {
			return
		}
		t.Fatal(err)
	}
	var q2 api.SetupQuestion
	if _, err := setupHooks(NewArgs{}, gi.Worktree, gi); !errors.As(err, &pe) || json.Unmarshal(pe.Data, &q2) != nil || q2.Kept != "" || !missing(gi.Worktree) {
		t.Errorf("a worktree that went: %v, kept %q", err, q2.Kept)
	}
}

// failingWriter fails each write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is closed") }

// git-op says what to try when it cannot write its answer.
func TestRunGitOpCannotWriteItsAnswer(t *testing.T) {
	t.Setenv(gitScopeEnv, gitNoScope)
	err := RunGitOp([]string{gitOpRepoInfo}, strings.NewReader(`{"dir":"`+t.TempDir()+`"}`), failingWriter{})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "git_op" || !strings.Contains(pe.Message, "could not write its answer: the pipe is closed") || pe.Hint == "" {
		t.Errorf("RunGitOp = %v", err)
	}
}
