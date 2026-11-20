package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The parent's side of a git operation: it starts the child, in its scope,
// and reads its answer. The child keeps the operation's deadline itself
// (RunGitOp), so the deadline holds also when the bridge that started it
// has gone. The parent waits gitOpGrace longer, for the child's own answer,
// and stops the child itself only when that does not come.

// gitOpGrace is how much longer than the operation's deadline the parent
// waits for the child's answer.
const gitOpGrace = 15 * time.Second

// liveGitOps are the git operations that this process runs now, each with
// the function that stops it, and a channel that closes when it ended.
// gitOpsStopped is true once StopGitOps ran: this process then starts no
// worktree.add, so that it makes nothing that no one would remove.
var (
	liveGitOpsMu  sync.Mutex
	liveGitOps    = map[*context.CancelFunc]chan struct{}{}
	gitOpsStopped bool
)

// StopGitOps stops every git operation that this process runs, with its
// scope or its process group, and waits up to 15 seconds for them to end.
// After it, the process starts no worktree.add. Other operations still
// start, such as the worktree.discard that removes what a stopped add had
// begun. EndBridge calls it when the bridge's connection ends, so that
// nothing that the bridge started runs on without it.
func StopGitOps() {
	liveGitOpsMu.Lock()
	gitOpsStopped = true
	done := make([]chan struct{}, 0, len(liveGitOps))
	for cancel, d := range liveGitOps {
		(*cancel)()
		done = append(done, d)
	}
	liveGitOpsMu.Unlock()
	timeout := time.After(15 * time.Second)
	for _, d := range done {
		select {
		case <-d:
		case <-timeout:
			return
		}
	}
}

// liveGitOp adds the operation op, which cancel stops, to liveGitOps, and
// returns the function that takes it off when it ended. After StopGitOps,
// it refuses a worktree.add.
func liveGitOp(op string, cancel context.CancelFunc) (ended func(), err error) {
	d := make(chan struct{})
	liveGitOpsMu.Lock()
	defer liveGitOpsMu.Unlock()
	if gitOpsStopped && op == gitOpWorktreeAdd {
		return nil, UserError("git_op", "the helper did not start git in "+op+" on this host, because its connection ended", gitOpHint)
	}
	liveGitOps[&cancel] = d
	return func() {
		liveGitOpsMu.Lock()
		delete(liveGitOps, &cancel)
		liveGitOpsMu.Unlock()
		close(d)
	}, nil
}

// liveRequests counts the bridge's requests that have not ended yet.
var liveRequests atomic.Int64

// requestDrain bounds how long EndBridge waits for the bridge's requests.
// It covers the removal of a half-made worktree: rollBackDeadline, then
// gitOpGrace. A project choice.
var requestDrain = rollBackDeadline + gitOpGrace + 15*time.Second

// EndBridge is the end of a bridge process, after its connection ended. It
// stops the git that the bridge's requests run (StopGitOps). It then waits
// up to requestDrain for those requests to end, so that a start whose add
// was stopped removes the half-made worktree before the process exits.
func EndBridge() {
	// Nothing reads the bridge's output any more. A request's reply there
	// would end the process with SIGPIPE before the other requests end: so
	// the write only fails.
	signal.Ignore(syscall.SIGPIPE)
	StopGitOps()
	for end := time.Now().Add(requestDrain); liveRequests.Load() > 0 && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
}

// gitOp runs one git operation in its child, within the operation's
// deadline, or within the request's, when it asks for a shorter one. See
// gitOpWithin.
func gitOp(op string, req gitOpRequest) (gitOpAnswer, error) {
	kind := findGitOp(op)
	if kind == nil {
		return gitOpAnswer{}, UserError("git_op", "the helper has no git operation "+op, gitOpHint)
	}
	return gitOpWithin(kind, req, kind.deadlineFor(req))
}

// gitTimeoutError says that the operation op ran past its deadline d.
func gitTimeoutError(op string, d time.Duration) error {
	return UserError(api.CodeGitTimeout,
		fmt.Sprintf("git ran longer than %s in %s on this host, so the helper stopped it", d, op),
		"A filter of the repository may hang. Check them on the host with `git config --show-origin --get-regexp '^filter[.]'`.")
}

// gitOpWithin runs one git operation in its child, in a scope when the host
// has a systemd user manager, and returns the child's answer. An error in
// the answer comes back as the error. The child stops git itself at the
// deadline. When it does not answer within gitOpGrace after that, or when
// StopGitOps stops it, the parent stops the scope, or the child's process
// group. The answer holds the plan of worktree.add, when the child sent one.
func gitOpWithin(kind *gitOpKind, req gitOpRequest, deadline time.Duration) (gitOpAnswer, error) {
	op := kind.name
	exe, err := holderExecutable()
	if err != nil {
		return gitOpAnswer{}, UserError("git_op", "could not find the helper to run git: "+err.Error(), gitOpHint)
	}
	req.DeadlineMS = deadline.Milliseconds()
	in, err := json.Marshal(req)
	if err != nil {
		return gitOpAnswer{}, UserError("git_op", "could not write the request of git-op "+op+": "+err.Error(), gitOpHint)
	}
	scoped, err := gitScopeWanted()
	if err != nil {
		return gitOpAnswer{}, err
	}
	scope, argv := gitOpArgv(exe, op, scoped)
	ctx, cancel := context.WithTimeout(context.Background(), deadline+gitOpGrace)
	defer cancel()
	ended, err := liveGitOp(op, cancel)
	if err != nil {
		return gitOpAnswer{}, err
	}
	defer ended()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), gitScopeEnv+"="+scope)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	// The child leads a process group of its own, which holds git and its
	// filters, so that the parent can stop all of them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if scope != gitNoScope {
			stopScope(scope)
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = gitOpWaitDelay
	runErr := cmd.Run()
	ans, plan, answered := readGitOpAnswers(out.Bytes())
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return gitOpAnswer{Plan: plan}, gitTimeoutError(op, deadline)
	case ctx.Err() != nil:
		return gitOpAnswer{Plan: plan}, UserError("git_op", "the helper stopped git in "+op+" on this host, because its connection ended", gitOpHint)
	case !answered:
		// No answer: the child did not start, or did not finish.
		why := firstLine(stderr.String())
		if why == "" && runErr != nil {
			why = runErr.Error()
		}
		if why == "" {
			why = "git-op gave no answer"
		}
		if scope != gitNoScope {
			return gitOpAnswer{Plan: plan}, gitScopeError(why)
		}
		return gitOpAnswer{Plan: plan}, UserError("git_op", "git-op "+op+" failed on this host: "+why, gitOpHint)
	}
	ans.Plan = plan
	if ans.Error != nil {
		return ans, ans.Error
	}
	return ans, nil
}

// readGitOpAnswers reads what a child wrote: a plan, maybe, and then the
// answer. answered is false when no answer came.
func readGitOpAnswers(out []byte) (ans gitOpAnswer, plan *GitInfo, answered bool) {
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var a gitOpAnswer
		if err := dec.Decode(&a); err != nil {
			return ans, plan, answered
		}
		if a.Plan != nil {
			plan = a.Plan
			continue
		}
		ans, answered = a, true
	}
}

// runGitOpChild runs a git operation in this process, the child, within its
// deadline, and writes its answer with enc. At the deadline, it answers
// git_timeout, and then stops itself and all that it started: git and its
// filters (stopOwnProcesses). It does this also when the bridge that
// started it has gone, and its answer has no reader.
func runGitOpChild(kind *gitOpKind, req gitOpRequest, enc *json.Encoder) error {
	// A write to a parent that has gone fails, and does not end the child
	// before it stops git.
	signal.Ignore(syscall.SIGPIPE)
	d := kind.deadlineFor(req)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	old := gitCtx.Swap(&ctx)
	defer gitCtx.Store(old)
	var mu sync.Mutex
	answered := false
	answer := func(a gitOpAnswer) error {
		mu.Lock()
		defer mu.Unlock()
		if answered {
			return nil
		}
		answered = true
		return writeGitOpAnswer(enc, kind.name, a)
	}
	timedOut := func() {
		_ = answer(gitOpAnswer{Error: asProtoError(gitTimeoutError(kind.name, d))})
		stopOwnProcesses()
	}
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			timedOut()
		}
	})
	defer stop()
	var planErr error
	ans, err := kind.run(req, func(g *GitInfo) {
		mu.Lock()
		defer mu.Unlock()
		planErr = enc.Encode(gitOpAnswer{Plan: g})
	})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		timedOut()
		return nil
	}
	if planErr != nil && err == nil {
		err = planErr
	}
	if err != nil {
		ans.Error = asProtoError(err)
	}
	return answer(ans)
}

// stopOwnProcesses stops the git operation's child and all that it started:
// its scope, or its process group, which gitOpWithin made it lead. A
// process that leads no group of its own, such as a test that runs the
// child's side in its own process, kills nothing.
func stopOwnProcesses() {
	if scope := os.Getenv(gitScopeEnv); scope != "" && scope != gitNoScope {
		stopScope(scope)
	}
	if syscall.Getpgrp() == os.Getpid() {
		_ = syscall.Kill(0, syscall.SIGKILL)
	}
}

// writeGitOpAnswer writes the answer of git-op op with enc.
func writeGitOpAnswer(enc *json.Encoder, op string, ans gitOpAnswer) error {
	if err := enc.Encode(ans); err != nil {
		return UserError("git_op", "git-op "+op+" could not write its answer: "+err.Error(),
			"Read the answer of git-op to its end, from its standard output.")
	}
	return nil
}
