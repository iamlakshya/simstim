package guest

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
)

// newOwnedCommand is also the future init-owned PTY's lifetime primitive.
// The caller chooses a lifetime; exec's ten-minute default lives outside it.
func newOwnedCommand(parent context.Context, a proto.CtlExecArgs, env []string) *execRun {
	ctx, cancel := context.WithCancel(parent)
	a.Argv = append([]string(nil), a.Argv...)
	return &execRun{args: a, env: append([]string(nil), env...), ctx: ctx, cancel: cancel, done: make(chan struct{}), procDone: make(chan struct{})}
}

func (g *Init) agentCommand(run *execRun, out, errout io.Writer) (*exec.Cmd, int, error) {
	var attr *syscall.SysProcAttr
	var err error
	if run.fileCommit != nil {
		attr, err = g.fileCommitProcess(run)
	} else {
		attr, err = g.commandProcess()
	}
	if err != nil {
		return nil, 125, err
	}
	path, code, err := guestExecutable(run.args.Argv[0], run.env, g.mapValue(run.args.Cwd))
	if err != nil {
		return nil, code, err
	}
	cmd := exec.Command(path, run.args.Argv[1:]...)
	cmd.Args = append([]string(nil), run.args.Argv...)
	cmd.Dir = g.mapValue(run.args.Cwd)
	cmd.Env = g.mapEnv(run.env)
	cmd.SysProcAttr = attr
	cmd.Stdout, cmd.Stderr = out, errout
	if run.fileCommit != nil {
		cmd.ExtraFiles = append([]*os.File(nil), run.fileCommit.files...)
	}
	return cmd, 0, nil
}

// startOwned linearises actual Start with init stop and registers the actual
// child handle before the general orphan reaper can see it.
func (g *Init) startOwned(run *execRun, cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended || run.ctx.Err() != nil {
		return execError("the guest is stopping before command start")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if g.vm != nil {
		release, err := g.vm.beforeOwnedStart(run, cmd)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	run.cmd = cmd
	if g.owned == nil {
		g.owned = make(map[*execRun]struct{})
	}
	g.owned[run] = struct{}{}
	return nil
}

// waitOwned has one waiter and sends the final group signal while the leader
// is still unreaped. No caller retains PID/group signal rights after Wait.
func (g *Init) waitOwned(run *execRun) (bool, error) {
	if g.purgeReader != nil {
		return g.purgeReader.wait(run)
	}
	run.waitOnce.Do(func() {
		_, run.waitKnown = proc.AwaitStatus(run.cmd.Process.Pid)
		run.mu.Lock()
		if g.vm != nil {
			g.vm.beforeOwnedReap(run)
		}
		if run.waitKnown {
			_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		}
		run.gone = true
		run.mu.Unlock()
		run.waitErr = run.cmd.Wait()
		if g.vm != nil {
			g.vm.afterOwnedReap(run)
		}
		g.mu.Lock()
		delete(g.owned, run)
		g.mu.Unlock()
		close(run.procDone)
	})
	return run.waitKnown, run.waitErr
}

func (g *Init) stopOwned() {
	g.mu.Lock()
	runs := make([]*execRun, 0, len(g.owned))
	for run := range g.owned {
		runs = append(runs, run)
	}
	g.mu.Unlock()
	for _, run := range runs {
		run.cancel()
		_ = run.signal(uint32(syscall.SIGKILL))
		if run.abortOutput != nil {
			run.abortOutput()
		}
	}
	for _, run := range runs {
		<-run.procDone
	}
}
