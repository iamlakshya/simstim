package guest

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// registerBundleCommand uses the same owner and records for pull. A zero
// timeout follows the operation's inactivity-controlled lifetime, not10minutes.
func (g *Init) registerBundleCommand(ctx context.Context, a proto.CtlExecArgs, env []string) error {
	_, err := g.registerCommand(ctx, a, env, link.KindBundle, "pull", false)
	return err
}

func (g *Init) registerCommand(parent context.Context, a proto.CtlExecArgs, env []string, kind link.Kind, purpose string, execTimeout bool) (*execRun, error) {
	check := api.ExecArgs{ID: a.ID, Argv: a.Argv, TimeoutMS: a.TimeoutMS}
	if err := check.Normalise(); err != nil {
		return nil, err
	}
	a.Argv = check.Argv
	if execTimeout {
		a.TimeoutMS = check.TimeoutMS
	}
	run := newOwnedCommand(parent, a, env)
	if a.TimeoutMS > 0 {
		baseCancel := run.cancel
		timed, stopTimer := context.WithTimeout(run.ctx, time.Duration(a.TimeoutMS)*time.Millisecond)
		run.ctx = timed
		run.cancel = func() { stopTimer(); baseCancel() }
	}
	run.kind, run.purpose = kind, purpose
	g.mu.Lock()
	if g.ended {
		g.mu.Unlock()
		run.cancel()
		return nil, execError("the guest is stopping")
	}
	if g.execs[a.ID] != nil || len(g.execs) >= link.DefaultMaxSupervisorStreams {
		g.mu.Unlock()
		run.cancel()
		return nil, execError("the command ID or stream slots are in use")
	}
	if g.execs == nil {
		g.execs = make(map[string]*execRun)
	}
	g.execs[a.ID] = run
	g.mu.Unlock()
	go func() {
		if g.sess != nil {
			select {
			case <-run.ctx.Done():
			case <-g.sess.Done():
				run.cancel()
			}
		} else {
			<-run.ctx.Done()
		}
		g.mu.Lock()
		if !run.taken {
			delete(g.execs, a.ID)
			close(run.done)
		}
		g.mu.Unlock()
		g.endIfEmpty()
	}()
	return run, nil
}

// runBundleProbe captures bounded Git metadata through the same physical owner.
// The bundle.prepare caller retains holdCommandOperation across all phases.
func (g *Init) runBundleProbe(ctx context.Context, a proto.CtlExecArgs, env []string, out, errout io.Writer) (int, error) {
	run, err := g.registerCommand(ctx, a, env, link.KindBundle, "pull", false)
	if err != nil {
		return 125, err
	}
	g.mu.Lock()
	if g.execs[a.ID] != run || run.taken {
		g.mu.Unlock()
		return 125, run.ctx.Err()
	}
	run.taken = true
	g.mu.Unlock()
	defer g.finishCommand(run)
	code := g.executeOwned(run, ownedCommandIO{Out: out, Err: errout})
	return code, run.ctx.Err()
}
func (g *Init) finishCommand(run *execRun) {
	run.cancel()
	g.mu.Lock()
	if g.execs[run.args.ID] == run {
		delete(g.execs, run.args.ID)
	}
	g.mu.Unlock()
	close(run.done)
	g.endIfEmpty()
}

func (g *Init) tryBundleCommand(st *link.Stream) bool {
	var h struct {
		Purpose string `json:"purpose"`
	}
	if st.Header(&h) != nil {
		st.Close()
		return true
	}
	if h.Purpose == "" {
		return false
	} // legacy token-only P16 seed path
	if h.Purpose != "pull" {
		st.Close()
		return true
	}
	go g.serveExec(st)
	return true
}

func (g *Init) holdCommandOperation() (func(), error) {
	g.mu.Lock()
	if g.ended || g.commandHolds >= link.DefaultMaxSupervisorStreams {
		g.mu.Unlock()
		return nil, execError("the guest cannot retain another command operation")
	}
	g.commandHolds++
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.commandHolds--; g.mu.Unlock(); g.endIfEmpty() }) }, nil
}
