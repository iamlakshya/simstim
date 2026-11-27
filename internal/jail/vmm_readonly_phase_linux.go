//go:build linux

package jail

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func (c *Cmd) waitReadonlyProjectionV1(ctx context.Context, owner *vmmExecOwnerV1) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-owner.readonlyProjectionReady:
	case <-owner.nativeDone:
		return vmmModeFailureV1("the original read-only chain ended before control adoption")
	}
	owner.recordMu.Lock()
	defer owner.recordMu.Unlock()
	owner.mu.Lock()
	if !owner.readonly || owner.projection || owner.init == nil || owner.runner == nil || !owner.readonlyControlTerminal || !owner.readonlyDiskAdopted {
		owner.mu.Unlock()
		return vmmModeFailureV1("the original read-only init has no complete projection/borrower chain")
	}
	init, reader, code := owner.init, owner.reader, owner.code
	namespaces := init.namespaces
	owner.mu.Unlock()
	if err := init.checkExecutableV1(code); err != nil {
		return err
	}
	if err := init.checkNamespacesV1(namespaces); err != nil {
		return err
	}
	if err := owner.checkReadonlyIDsV1(init, true); err != nil {
		return err
	}
	stop, err := vmmReadContextV1(ctx, reader)
	if err != nil {
		return err
	}
	defer stop()
	record, err := supply.ReadExecModeRecordV1(reader)
	if err != nil {
		return err
	}
	if record.PID != 1 {
		return vmmModeFailureV1("the read-only projection is not the original namespace-init")
	}
	if err := owner.source.CheckProjectionRecordV1(ctx, record); err != nil {
		return err
	}
	if err := init.checkExecutableV1(code); err != nil {
		return err
	}
	if err := init.checkNamespacesV1(namespaces); err != nil {
		return err
	}
	if err := owner.checkReadonlyIDsV1(init, true); err != nil {
		return err
	}
	owner.mu.Lock()
	owner.projection = true
	writer := owner.writer
	owner.writer = nil
	owner.mu.Unlock()
	if writer == nil {
		return vmmModeFailureV1("the original read-only projection writer is missing")
	}
	return writer.Close()
}

func (c *Cmd) ContinueVMMReadonlyInitV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.readonly || !owner.projection || owner.readonlyContinued || !owner.readonlyDiskAdopted || !owner.readonlyControlTerminal || owner.nativeErr != nil {
		return vmmModeFailureV1("the original read-only init has no native continuation boundary")
	}
	if err := owner.root.live(); err != nil {
		return err
	}
	if err := owner.init.checkExecutableV1(owner.code); err != nil {
		return err
	}
	if err := owner.checkReadonlyIDsV1(owner.init, true); err != nil {
		return err
	}
	owner.readonlyContinued = true // A failed send remains uncertain, never unstarted.
	_, err = owner.host.Write([]byte(vmmReadonlyExecContinueV1))
	return err
}

func (actor *vmmExecActorV1) readonlyExecutableV1(helper, program *os.File) (bool, error) {
	if err := actor.live(); err != nil {
		return false, err
	}
	fd, err := unix.Openat(int(actor.task.Fd()), "exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	var actual, code, want unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &actual), unix.Fstat(int(helper.Fd()), &code), unix.Fstat(int(program.Fd()), &want)); err != nil {
		return false, err
	}
	if actual.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, vmmModeFailureV1("the original init executable has another native type")
	}
	final := actual.Dev == want.Dev && actual.Ino == want.Ino && actual.Size == want.Size
	initial := actual.Dev == code.Dev && actual.Ino == code.Ino && actual.Size == code.Size
	if !final && !initial {
		return false, vmmModeFailureV1("the original init entered another actual executable")
	}
	return final, actor.live()
}

// Positive code/namespace/actual-ID observation is required. A live known
// helper may still be waiting; EACCES, a third executable, death or EOF fails.
func (c *Cmd) WaitVMMReadonlyFinalV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.recordMu.Lock()
	defer owner.recordMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		owner.mu.Lock()
		if !owner.readonly || !owner.readonlyContinued || owner.init == nil || owner.nativeErr != nil {
			owner.mu.Unlock()
			return vmmModeFailureV1("the original read-only final phase ended or was never continued")
		}
		init, code, program, namespaces := owner.init, owner.code, owner.program, owner.init.namespaces
		if err := owner.root.live(); err != nil {
			owner.mu.Unlock()
			return err
		}
		final, err := init.readonlyExecutableV1(code, program)
		if err == nil {
			err = init.checkNamespacesV1(namespaces)
		}
		if err == nil {
			err = owner.checkReadonlyIDsV1(init, !final)
		}
		if err == nil && final {
			owner.finalProved = true
			owner.mu.Unlock()
			return nil
		}
		owner.mu.Unlock()
		if err != nil {
			return err
		}
		raw, err := init.process.SyscallConn()
		if err != nil {
			return err
		}
		var ended bool
		var native error
		err = raw.Control(func(fd uintptr) { ended, native = ownedPIDFDReady(int(fd), 10) })
		if err != nil || native != nil || ended {
			return errors.Join(err, native, vmmModeFailureV1("the original init ended before positive final-code observation"))
		}
	}
}
