//go:build linux

package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/proc"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

type purgeReaderProbeV1 struct {
	run      *execRun
	command  *exec.Cmd
	parent   net.Conn
	child    *os.File
	original *os.File
	err      error
}

func (p *purgeReaderV1) probe(ctx context.Context, directory string, arguments ...string) (string, int, error) {
	request := purgeReaderGitRequestV1{Directory: directory, Arguments: append([]string(nil), arguments...)}
	if err := validatePurgeReaderGitV1(request); err != nil {
		return "", 125, err
	}
	if err := p.connection.check(p.init); err != nil {
		return "", 125, err
	}
	id, err := bundleNonce()
	if err != nil {
		return "", 125, err
	}
	args := proto.CtlExecArgs{ID: id, Cwd: "/", Argv: []string{"/.tools/init", PurgeReaderGitHelperModeV1}}
	run, err := p.init.registerCommand(ctx, args, bundleEnvironment(), "", "purge-reader", false)
	if err != nil {
		return "", 125, err
	}
	p.init.mu.Lock()
	run.taken = true
	p.init.mu.Unlock()
	defer p.init.finishCommand(run)
	probe := &purgeReaderProbeV1{run: run}
	p.mu.Lock()
	if p.active != nil || p.failure != nil {
		p.mu.Unlock()
		return "", 125, purgeWorkErrorV1("the fixed reader already owns an unfinished command")
	}
	p.active = probe
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active = nil
		p.mu.Unlock()
	}()
	if err := probe.openV1(); err != nil {
		if closeErr := probe.closeV1(); closeErr != nil {
			p.retainV1(errors.Join(err, closeErr))
		}
		return "", 125, err
	}
	output, diagnostic := &bundleText{remaining: 1 << 20}, &bundleText{remaining: 64 << 10}
	cmd := exec.Command(args.Argv[0], args.Argv[1:]...)
	cmd.Dir, cmd.Env = "/", bundleEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Stdout, cmd.Stderr, cmd.ExtraFiles = output, diagnostic, []*os.File{probe.child}
	probe.command = cmd // actual object retained before the fallible native start.
	if err := p.init.startOwned(run, cmd); err != nil {
		if cmd.Process != nil {
			p.retainV1(err) // late/ambiguous birth is cleanup-only, never a guessed signal.
		}
		if closeErr := probe.closeV1(); closeErr != nil {
			p.retainV1(errors.Join(err, closeErr))
		}
		return "", execStartCode(err), err
	}
	if err := probe.child.Close(); err != nil {
		p.retainV1(err)
	}
	probe.child = nil
	cancelStop, cancelJoined := make(chan struct{}), make(chan struct{})
	control := probe.parent
	go func() {
		defer close(cancelJoined)
		select {
		case <-run.ctx.Done():
			_ = run.signal(uint32(syscall.SIGKILL))
			_ = control.SetDeadline(time.Now())
		case <-cancelStop:
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = probe.parent.SetDeadline(deadline)
	}
	raw, marshalErr := json.Marshal(request)
	if marshalErr == nil && len(raw) <= 64<<10 {
		marshalErr = proto.NewWriter(probe.parent).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: purgeReaderGitOperationV1, Args: raw})
	} else if marshalErr == nil {
		marshalErr = purgeWorkErrorV1("the original fixed Git request exceeds its bound")
	}
	if marshalErr != nil {
		run.cancel()
	}
	known, waitErr := p.wait(run)
	close(cancelStop)
	<-cancelJoined
	if closeErr := probe.closeV1(); closeErr != nil {
		p.retainV1(closeErr)
	}
	if !known || marshalErr != nil {
		return "", 125, errors.Join(marshalErr, waitErr)
	}
	if err := run.ctx.Err(); err != nil {
		return "", execCancelled(run.ctx), err
	}
	return output.data.String(), execWaitCode(waitErr), nil
}

func (r *purgeReaderProbeV1) openV1() error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	left := os.NewFile(uintptr(fds[0]), "original-reader-parent")
	r.original = left
	r.child = os.NewFile(uintptr(fds[1]), "original-reader-child")
	connection, err := net.FileConn(left)
	closed := left.Close()
	if closed == nil {
		r.original = nil
	}
	r.err = errors.Join(r.err, closed)
	r.parent = connection
	return errors.Join(err, closed)
}

func (r *purgeReaderProbeV1) closeV1() error {
	if r.err != nil {
		return r.err // Preserve the same ambiguous close; never retry a retired numeric descriptor.
	}
	if r.child != nil {
		err := r.child.Close()
		if err == nil {
			r.child = nil
		}
		r.err = errors.Join(r.err, err)
	}
	if r.parent != nil {
		err := r.parent.Close()
		if err == nil {
			r.parent = nil
		}
		r.err = errors.Join(r.err, err)
	}
	return r.err
}

// The ordinary wait path remains unchanged. Only this actual reader purpose
// requires its same native observation and final group result before Wait.
func (p *purgeReaderV1) wait(run *execRun) (bool, error) {
	run.waitOnce.Do(func() {
		status, known, err := proc.AwaitStatusError(run.cmd.Process.Pid)
		if err != nil || !known {
			p.retainV1(errors.Join(purgeWorkErrorV1("the fixed reader lost its original native command observation"), err))
		}
		run.mu.Lock()
		if run.gone {
			run.mu.Unlock()
			p.retainV1(purgeWorkErrorV1("the fixed reader command was already reaped"))
		}
		groupErr := syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		if groupErr != nil && !errors.Is(groupErr, syscall.ESRCH) {
			run.mu.Unlock()
			p.retainV1(groupErr)
		}
		run.waitErr = run.cmd.Wait()
		run.gone = true
		actual := run.cmd.ProcessState
		run.mu.Unlock()
		var exit *exec.ExitError
		if run.waitErr != nil && !errors.As(run.waitErr, &exit) || actual == nil {
			p.retainV1(errors.Join(purgeWorkErrorV1("the fixed reader command and pipes did not positively join"), run.waitErr))
		}
		actualStatus, ok := actual.Sys().(syscall.WaitStatus)
		if !ok || actualStatus != status {
			p.retainV1(purgeWorkErrorV1("the fixed reader's original native observation and Wait disagree"))
		}
		run.waitKnown = true
		p.init.mu.Lock()
		delete(p.init.owned, run)
		p.init.mu.Unlock()
		close(run.procDone)
	})
	return run.waitKnown, run.waitErr
}
