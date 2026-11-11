package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// slowFilterRepo makes a repository whose README runs a filter, of kind
// "clean" or "smudge", that writes its process ID to pids, and sleeps first
// script, then for a minute.
func slowFilterRepo(t *testing.T, kind, first string) (repo, pids string) {
	t.Helper()
	repo = setupRepo(t)
	marks := t.TempDir()
	pids = filepath.Join(marks, "pids")
	put(t, filepath.Join(marks, "slow.sh"), "#!/bin/sh\necho $$ >> "+pids+"\n"+first+"sleep 60\ncat\n", 0o755)
	put(t, filepath.Join(repo, "README"), "app\n", 0o644)
	put(t, filepath.Join(repo, ".gitattributes"), "README filter=slow\n", 0o644)
	for _, args := range [][]string{{"add", "README", ".gitattributes"}, {"commit", "-q", "-m", "attributes"}, {"config", "filter.slow." + kind, filepath.Join(marks, "slow.sh")}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo, pids
}

// The child keeps the deadline itself: when the bridge that started an add
// has gone, git and its filter still end at the deadline.
func TestGitDeadlineHoldsWithoutTheBridge(t *testing.T) {
	skipOnGit254(t)
	repo, pids := slowFilterRepo(t, "smudge", "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, gitScopeEnv+"=") })
	attempt := 0
	tryDeadlines(t, findGitOp(gitOpWorktreeAdd), func(d time.Duration) bool {
		attempt++
		_ = os.Remove(pids)
		// The bridge: a git-op that starts the child, as the client's one
		// over SSH does, and then dies.
		bridge := exec.Command(exe, "git-op", gitOpWorktreeAdd)
		bridge.Env = env
		bridge.Stdin = strings.NewReader(fmt.Sprintf(`{"add":{"dir":%q,"name":"orphan","id":"abcd234%d"},"deadline_ms":%d}`, repo, attempt, d.Milliseconds()))
		if err := bridge.Start(); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		for len(logLines(pids)) == 0 && time.Since(started) < d {
			time.Sleep(50 * time.Millisecond)
		}
		_ = bridge.Process.Kill()
		_ = bridge.Wait()
		if len(logLines(pids)) == 0 {
			return false
		}
		// filterGone waits up to 15 s, which with the deadline's rest is
		// enough for a child that keeps the deadline itself.
		time.Sleep(time.Until(started.Add(d)))
		filterGone(t, logLines(pids))
		return true
	})
}

// StopGitOps, which the bridge runs when its connection ends, stops a git
// operation at once, with its filter, and the operation says so.
func TestStopGitOps(t *testing.T) {
	skipOnGit254(t)
	t.Cleanup(startGitOpsAgain)
	repo, pids := slowFilterRepo(t, "clean", "")
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := gitOp(gitOpRepoInfo, repoReq(repo, true))
		done <- err
	}()
	hostWaitFor(t, 30*time.Second, func() bool { return len(logLines(pids)) > 0 })
	StopGitOps()
	select {
	case err := <-done:
		var pe *proto.Error
		if !errors.As(err, &pe) || !strings.Contains(pe.Message, "because its connection ended") || pe.Hint == "" {
			t.Errorf("gitOp = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("StopGitOps did not stop the operation")
	}
	filterGone(t, logLines(pids))
}

// hostWaitFor polls cond until it is true, or fails the test.
func hostWaitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal("timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A smudge filter that makes the config unacceptable during the checkout,
// with a hook named "a=b", and then hangs: the removal after the deadline
// cannot run git, so it says that the worktree and its branch stay. A
// branch whose state git cannot tell does not count as gone.
func TestRollBackWithAConfigThatTurnsBad(t *testing.T) {
	skipOnGit254(t)
	repo, pids := slowFilterRepo(t, "smudge", "")
	put(t, filepath.Join(filepath.Dir(pids), "slow.sh"), "#!/bin/sh\necho $$ >> "+pids+"\n"+
		"printf '[hook \"a=b\"]\\n\\tcommand = true\\n' >> "+filepath.Join(repo, ".git", "config")+"\nsleep 60\ncat\n", 0o755)
	attempt := 0
	tryDeadlines(t, findGitOp(gitOpWorktreeAdd), func(time.Duration) bool {
		attempt++
		id := fmt.Sprintf("efgh234%d", attempt)
		_ = os.Remove(pids)
		_, err := makeWorktree(repo, "bad", id, "")
		ran := logLines(pids)
		filterGone(t, ran)
		if len(ran) == 0 {
			return false
		}
		var pe *proto.Error
		want := fmt.Sprintf("(branch %s/bad-%s) is still on the host", brand.Name, id)
		if !errors.As(err, &pe) || pe.Code != api.CodeGitTimeout || !strings.Contains(pe.Message, want) {
			t.Errorf("makeWorktree = %v, want git_timeout that says %q", err, want)
		}
		return true
	})
}

// The command that a kept worktree's note gives removes the worktree and its
// branch, also a locked worktree whose path has a space and a quote.
func TestRemoveCommandRemovesALockedWorktree(t *testing.T) {
	repo := setupRepo(t)
	wt := filepath.Join(t.TempDir(), "it's a worktree")
	branch := brand.Name + "/x-abcd2345"
	for _, args := range [][]string{{"worktree", "add", "-q", "-b", branch, wt}, {"worktree", "lock", wt}} {
		if out, err := git(repo, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	g := &GitInfo{RepoRoot: repo, Worktree: wt, Branch: branch}
	note := keptNote(g)
	cmdline := removeCommand(g)
	if !strings.Contains(note, "`"+cmdline+"`") {
		t.Fatalf("the note %q does not give %q", note, cmdline)
	}
	if out, err := exec.Command("/bin/sh", "-c", cmdline).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmdline, err, out)
	}
	if !missing(wt) {
		t.Error("the locked worktree is still there")
	}
	if gone, err := branchMissing(repo, branch); err != nil || !gone {
		t.Errorf("the branch is still there: %v", err)
	}
	if strings.Contains(git2(t, repo, "worktree", "list"), "it's a worktree") {
		t.Error("git still lists the worktree")
	}
}

// git2 runs git in dir for a test, and returns its output.
func git2(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// startGitOpsAgain undoes StopGitOps for the tests that follow.
func startGitOpsAgain() {
	liveGitOpsMu.Lock()
	gitOpsStopped = false
	liveGitOpsMu.Unlock()
}

// An add that fails after it began, here because a required filter fails in
// the checkout, removes the branch that it made. git itself removes the
// folder.
func TestAFailedAddRemovesItsBranch(t *testing.T) {
	skipOnGit254(t)
	repo, pids := slowFilterRepo(t, "smudge", "exit 1\n")
	git2(t, repo, "config", "filter.slow.required", "true")
	_, err := makeWorktree(repo, "x", "fail2345", "")
	if len(logLines(pids)) == 0 {
		t.Fatalf("the filter did not run: %v", err)
	}
	if err == nil {
		t.Fatal("makeWorktree made a worktree although its filter failed")
	}
	branch := brand.Name + "/x-fail2345"
	if gone, berr := branchMissing(repo, branch); berr != nil || !gone {
		t.Errorf("the branch %s stays after the add failed with %v (%v)", branch, err, berr)
	}
	if list := git2(t, repo, "worktree", "list"); strings.Contains(list, "x-fail2345") {
		t.Errorf("git still lists the worktree: %s", list)
	}
	if !missing(filepath.Join(WorktreesDir(), filepath.Base(repo), "x-fail2345")) {
		t.Error("the worktree's folder stays")
	}
}

// An add never removes a branch or a folder that was there before it: it
// refuses to start, and they stay.
func TestAnAddKeepsWhatWasThere(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	branch := brand.Name + "/x-have2345"
	git2(t, repo, "branch", branch)
	_, err := makeWorktree(repo, "x", "have2345", "")
	var pe *proto.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "is already in") || pe.Hint == "" {
		t.Errorf("makeWorktree with its branch there = %v", err)
	}
	if gone, berr := branchMissing(repo, branch); berr != nil || gone {
		t.Errorf("the add removed the branch that was there (%v)", berr)
	}
	wt := filepath.Join(WorktreesDir(), filepath.Base(repo), "y-have2345")
	put(t, filepath.Join(wt, "keep"), "mine\n", 0o644)
	_, err = makeWorktree(repo, "y", "have2345", "")
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "is already there") || pe.Hint == "" {
		t.Errorf("makeWorktree with its folder there = %v", err)
	}
	if missing(filepath.Join(wt, "keep")) {
		t.Error("the add removed the folder that was there")
	}
}

// When the bridge's connection ends during an add's checkout, EndBridge
// stops the add, and waits for the start, which removes the half-made
// worktree and its branch. After that, the process starts no add.
func TestEndBridgeRemovesAStoppedAdd(t *testing.T) {
	skipOnGit254(t)
	t.Cleanup(startGitOpsAgain)
	repo, pids := slowFilterRepo(t, "smudge", "")
	done := make(chan error, 1)
	liveRequests.Add(1)
	go func() {
		defer liveRequests.Add(-1)
		_, err := makeWorktree(repo, "x", "drop2345", "")
		done <- err
	}()
	hostWaitFor(t, 30*time.Second, func() bool { return len(logLines(pids)) > 0 })
	EndBridge()
	select {
	case err := <-done:
		var pe *proto.Error
		if !errors.As(err, &pe) || !strings.Contains(pe.Message, "because its connection ended") || strings.Contains(pe.Message, "still on the host") {
			t.Errorf("makeWorktree = %v, want a stop without a kept worktree", err)
		}
	default:
		t.Fatal("EndBridge returned while the start still ran")
	}
	filterGone(t, logLines(pids))
	if gone, err := branchMissing(repo, brand.Name+"/x-drop2345"); err != nil || !gone {
		t.Errorf("the branch of the stopped add stays (%v)", err)
	}
	if list := git2(t, repo, "worktree", "list"); strings.Contains(list, "x-drop2345") {
		t.Errorf("git still lists the worktree: %s", list)
	}
	if !missing(filepath.Join(WorktreesDir(), filepath.Base(repo), "x-drop2345")) {
		t.Error("the half-made worktree's folder stays")
	}
	_, err := makeWorktree(repo, "y", "late2345", "")
	if err == nil || !strings.Contains(err.Error(), "did not start git in worktree.add") {
		t.Errorf("an add after EndBridge = %v", err)
	}
}

// A bridge that its client left idle ends its git operations before it
// exits, as at the end of its connection.
func TestTheIdleExitStopsGit(t *testing.T) {
	skipOnGit254(t)
	t.Cleanup(startGitOpsAgain)
	exited := make(chan struct{}, 1)
	exitProcess = func(int) { exited <- struct{}{} }
	t.Cleanup(func() { exitProcess = os.Exit })
	repo, pids := slowFilterRepo(t, "clean", "")
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := gitOp(gitOpRepoInfo, repoReq(repo, true))
		done <- err
	}()
	hostWaitFor(t, 30*time.Second, func() bool { return len(logLines(pids)) > 0 })
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	_ = BridgeInput(r, []string{"--idle-exit", "1"})
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the idle bridge did not exit")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "because its connection ended") {
			t.Errorf("gitOp = %v", err)
		}
	default:
		t.Fatal("the idle bridge exited while its git operation still ran")
	}
	filterGone(t, logLines(pids))
}

// branchMissing counts a branch as gone only by git's own answer. Outside
// a repository, git fails in another way, and that is an error.
func TestBranchMissingOutsideARepository(t *testing.T) {
	if gone, err := branchMissing(t.TempDir(), "x"); err == nil {
		t.Errorf("branchMissing outside a repository = %v, <nil>", gone)
	}
}

// A start whose add the end of the bridge's connection stops: the bridge
// counts the start as a live request, so EndBridge waits for it, and the
// start removes the half-made worktree and its branch first.
func TestBridgeWaitsForItsRequests(t *testing.T) {
	skipOnGit254(t)
	t.Cleanup(startGitOpsAgain)
	repo, pids := slowFilterRepo(t, "smudge", "")
	in, w := io.Pipe()
	ended := make(chan error, 1)
	go func() { ended <- RunBridge(in, io.Discard) }()
	pw := proto.NewWriter(w)
	if err := pw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "client"}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(NewArgs{Name: "x", Cwd: repo, Worktree: true, Argv: []string{"sleep", "60"}})
	if err := pw.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "new", Args: args}); err != nil {
		t.Fatal(err)
	}
	hostWaitFor(t, 60*time.Second, func() bool { return len(logLines(pids)) > 0 })
	_ = w.Close()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge did not end when its input closed")
	}
	EndBridge()
	filterGone(t, logLines(pids))
	if out := git2(t, repo, "branch", "--list", brand.Name+"/*"); out != "" {
		t.Errorf("the stopped start left its branch: %s", out)
	}
	if list := git2(t, repo, "worktree", "list"); strings.Count(list, "\n") > 0 {
		t.Errorf("git still lists the half-made worktree: %s", list)
	}
	if left, _ := os.ReadDir(filepath.Join(WorktreesDir(), filepath.Base(repo))); len(left) > 0 {
		t.Errorf("the half-made worktree's folder stays: %v", left)
	}
}

// endBridgeMark makes TestHelperEndBridgeReply the child's program.
const endBridgeMark = "END_BRIDGE_MARK"

// After EndBridge, a request's reply to the client that has gone fails. It
// does not end the process with SIGPIPE while other requests still run.
func TestEndBridgeSurvivesAClosedOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mark := filepath.Join(t.TempDir(), "survived")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // no reader: a write to w fails with EPIPE
	cmd := exec.Command(exe, "-test.run=^TestHelperEndBridgeReply$")
	cmd.Env = append(os.Environ(), endBridgeMark+"="+mark)
	cmd.Stdout = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	_ = cmd.Wait()
	if missing(mark) {
		t.Errorf("the process ended at its first write to the closed output: %v", cmd.ProcessState)
	}
}

// TestHelperEndBridgeReply is the child of TestEndBridgeSurvivesAClosedOutput,
// and not a test of its own: a bridge whose connection ended, and that then
// writes a reply.
func TestHelperEndBridgeReply(t *testing.T) {
	mark := os.Getenv(endBridgeMark)
	if mark == "" {
		t.Skip("only TestEndBridgeSurvivesAClosedOutput runs this")
	}
	EndBridge()
	_, err := os.Stdout.WriteString("a reply\n")
	if err := os.WriteFile(mark, []byte(fmt.Sprint(err)), 0o600); err != nil {
		t.Fatal(err)
	}
}
