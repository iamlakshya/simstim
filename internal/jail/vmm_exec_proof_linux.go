//go:build linux

package jail

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func (owner *vmmExecOwnerV1) readNativeV1() {
	defer close(owner.nativeDone)
	host := owner.host
	for {
		var raw [16]byte
		n, control, err := proto.ReceiveUnixControlV1(host, raw[:], 4)
		if err == nil && n == 0 {
			err = io.EOF
		}
		if err == nil {
			err = control.CheckReceivedV1(host)
		}
		if err == nil {
			var message vmmNativeMessageV1
			message, err = readVMMNativeMessageV1(raw[:n])
			if err == nil {
				err = owner.acceptNativeV1(message, &control)
			}
		}
		control.Close()
		if err != nil {
			owner.mu.Lock()
			if !owner.finalProved && !owner.noFuture {
				owner.nativeErr = err
			}
			owner.mu.Unlock()
			owner.runnerOnce.Do(func() { close(owner.runnerReady) })
			_ = owner.closeHostV1()
			return
		}
		if _, err = host.Write([]byte{0xa6}); err != nil {
			owner.mu.Lock()
			owner.nativeErr = err
			owner.mu.Unlock()
			owner.runnerOnce.Do(func() { close(owner.runnerReady) })
			_ = owner.closeHostV1()
			return
		}
	}
}
func (owner *vmmExecOwnerV1) acceptNativeV1(message vmmNativeMessageV1, control *proto.UnixControlV1) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if control.Credentials == nil {
		return vmmModeFailureV1("the original creation record has no actual kernel sender")
	}
	var actor *vmmExecActorV1
	for _, current := range owner.actors {
		if current.pid == control.Credentials.PID {
			actor = current
			break
		}
	}
	if actor == nil {
		return vmmModeFailureV1("the native stage sender has no original creation handle")
	}
	if err := actor.live(); err != nil {
		return err
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if actor.stage >= vmmStageExecV1 || message.kind != vmmModeEnterV1 && actor.stage == vmmStageInitV1 {
		uid, gid = owner.subUID, owner.subGID
	}
	if control.Credentials.UID != uid || control.Credentials.GID != gid {
		return vmmModeFailureV1("the original native stage changed its mapped creator identity")
	}
	_, namespace, err := vmmPIDFDIdentityV1(actor.process)
	if err != nil {
		return err
	}
	if namespace[len(namespace)-1] != int(message.localPID) {
		return vmmModeFailureV1("the stage record differs from its actual creation namespace task")
	}
	switch message.kind {
	case vmmModeEnterV1:
		runner := message.stage == vmmStageRunnerV1 && actor.stage == vmmStageExecV1 && actor.entered && owner.runner == nil
		if !runner && (actor.entered || message.stage != actor.stage) || len(control.Files) != 4 {
			return vmmModeFailureV1("the original helper entered another or duplicate native stage")
		}
		actual, err := control.Files[0].Stat()
		if err != nil {
			return err
		}
		expected, err := owner.code.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(actual, expected) {
			return vmmModeFailureV1("the actual native creator runs another helper executable")
		}
		for index, file := range control.Files[1:] {
			var fs unix.Statfs_t
			if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
				return err
			}
			if fs.Type != unix.NSFS_MAGIC {
				return vmmModeFailureV1("the helper namespace evidence is not native nsfs")
			}
			kind, err := unix.IoctlRetInt(int(file.Fd()), unix.NS_GET_NSTYPE)
			if err != nil {
				return err
			}
			if kind != []int{unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNS}[index] {
				return vmmModeFailureV1("the helper namespace evidence changed its exact native role")
			}
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if runner && !os.SameFile(info, actor.namespaces[index]) {
				return vmmModeFailureV1("the re-executed runner changed its original native namespace")
			}
			actor.namespaces[index] = info
		}
		if actor.stage == vmmStageInitV1 && (len(namespace) < 2 || namespace[len(namespace)-1] != 1) {
			return vmmModeFailureV1("the actual init creation is not PID1 of its child namespace")
		}
		if actor.stage == vmmStageExecV1 && owner.init != nil {
			for index, info := range actor.namespaces {
				if !os.SameFile(info, owner.init.namespaces[index]) {
					return vmmModeFailureV1("the actual runner child escaped its original init namespace")
				}
			}
		}
		if err := owner.checkHelperActorV1(actor, actor.namespaces); err != nil {
			return err
		}
		actor.entered = true
		if actor.stage == vmmStageInitV1 {
			owner.init = actor
		}
		if runner {
			actor.stage = vmmStageRunnerV1
			owner.runner = actor
			owner.runnerOnce.Do(func() { close(owner.runnerReady) })
		}
	case vmmModeCreateV1:
		if !actor.entered || actor.issued || len(control.Files) != 1 || actor.stage != vmmStageOuterV1 && actor.stage != vmmStageInitV1 {
			return vmmModeFailureV1("the actual native creator cannot issue another helper")
		}
		want := vmmStageExecV1
		if actor.stage == vmmStageOuterV1 {
			want = vmmStageInitV1
		}
		if message.stage != want {
			return vmmModeFailureV1("the native creator issued another helper purpose")
		}
		rights, err := control.DetachFilesV1()
		if err != nil {
			return err
		}
		child, err := captureVMMActorV1(rights[0])
		if err != nil {
			rights[0].Close()
			return err
		}
		state, err := readVMMTaskV1(child.task)
		if err != nil || state.parent != actor.pid {
			child.close()
			return errors.Join(err, vmmModeFailureV1("the atomic helper creation has another actual native parent"))
		}
		if err := actor.live(); err != nil {
			child.close()
			return err
		}
		child.stage = want
		actor.issued = true
		owner.actors = append(owner.actors, child)
	case vmmModeNoFutureV1:
		if !actor.entered || !actor.issued || actor.terminal || message.stage != actor.stage || len(control.Files) != 0 || actor.stage != vmmStageOuterV1 && actor.stage != vmmStageInitV1 {
			return vmmModeFailureV1("the native creator has no fixed no-future-exec boundary")
		}
		if err := owner.checkHelperActorV1(actor, actor.namespaces); err != nil {
			return err
		}
		actor.terminal = true
	default:
		return vmmModeFailureV1("the original native stage record is unknown")
	}
	return nil
}

// AcceptVMMExecChildV1 adopts only the actual original runner's received atomic
// creation descriptor. The SCM witness, mapped sender, parent, code closure and
// earlier native helper chain must all agree before a last-helper record counts.
func (c *Cmd) AcceptVMMExecChildV1(ctx context.Context, source *supply.ExecModeLeaseV1, conn *net.UnixConn, control *proto.UnixControlV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-owner.runnerReady:
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.runner == nil || owner.final != nil || owner.control != conn || control == nil || len(control.Files) != 1 || control.Credentials == nil {
		return vmmModeFailureV1("the final helper has no unique original native runner")
	}
	if err := control.CheckReceivedV1(conn); err != nil {
		return err
	}
	if control.Credentials.PID != owner.runner.pid || control.Credentials.UID != owner.subUID || control.Credentials.GID != owner.subGID {
		return vmmModeFailureV1("the final helper came from another actual creator")
	}
	if err := owner.runner.live(); err != nil {
		return err
	}
	rights, err := control.DetachFilesV1()
	if err != nil {
		return err
	}
	actor, err := captureVMMActorV1(rights[0])
	if err != nil {
		rights[0].Close()
		return err
	}
	state, err := readVMMTaskV1(actor.task)
	if err != nil || state.parent != owner.runner.pid {
		actor.close()
		return errors.Join(err, vmmModeFailureV1("the final helper has another original creation ancestry"))
	}
	if err := owner.runner.live(); err != nil {
		actor.close()
		return err
	}
	if err := actor.checkExecutableV1(owner.code); err != nil {
		actor.close()
		return err
	}
	if err := actor.checkNamespacesV1(owner.runner.namespaces); err != nil {
		actor.close()
		return err
	}
	owner.final = actor
	owner.actors = append(owner.actors, actor)
	close(owner.finalReady)
	return nil
}

func (c *Cmd) WaitVMMProjectionV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-owner.finalReady:
	case <-owner.nativeDone:
		owner.mu.Lock()
		nativeErr := owner.nativeErr
		owner.mu.Unlock()
		return errors.Join(nativeErr, vmmModeFailureV1("the original native VMM chain ended before final-child adoption"))
	}
	owner.recordMu.Lock()
	defer owner.recordMu.Unlock()
	owner.mu.Lock()
	if owner.projection || owner.final == nil {
		owner.mu.Unlock()
		return vmmModeFailureV1("the last VMM projection has another original phase")
	}
	final, reader, code := owner.final, owner.reader, owner.code
	namespaces := owner.runner.namespaces
	owner.mu.Unlock()
	if err := final.checkExecutableV1(code); err != nil {
		return err
	}
	if err := final.checkNamespacesV1(namespaces); err != nil {
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
	_, namespace, err := vmmPIDFDIdentityV1(final.process)
	if err != nil {
		return err
	}
	if record.PID != uint64(namespace[len(namespace)-1]) {
		return vmmModeFailureV1("the last projection record came from another actual helper")
	}
	if err := source.CheckProjectionRecordV1(ctx, record); err != nil {
		return err
	}
	if err := final.live(); err != nil {
		return err
	}
	if err := final.checkExecutableV1(code); err != nil {
		return err
	}
	if err := final.checkNamespacesV1(namespaces); err != nil {
		return err
	}
	owner.mu.Lock()
	owner.projection = true
	writer := owner.writer
	owner.writer = nil
	owner.mu.Unlock()
	return writer.Close()
}
func (c *Cmd) ArmVMMExecModeV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.projection || owner.armed || owner.final == nil || owner.runner == nil || owner.nativeErr != nil {
		return vmmModeFailureV1("the original helper chain has no last readonly projection proof")
	}
	if err := owner.final.live(); err != nil {
		return err
	}
	if err := owner.runner.live(); err != nil {
		return err
	}
	owner.armed = true // A failed chmod remains uncertain and retains every owner.
	return source.ArmV1(ctx)
}
func (c *Cmd) WaitVMMFinalBoundaryV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.recordMu.Lock()
	defer owner.recordMu.Unlock()
	owner.mu.Lock()
	if !owner.armed || owner.final == nil {
		owner.mu.Unlock()
		return vmmModeFailureV1("the original final exec was never armed")
	}
	reader, final, program := owner.reader, owner.final, owner.program
	namespaces := owner.runner.namespaces
	owner.mu.Unlock()
	stop, err := vmmReadContextV1(ctx, reader)
	if err != nil {
		return err
	}
	defer stop()
	record, err := supply.ReadExecModeRecordV1(reader)
	if err != nil {
		return err
	}
	_, namespace, err := vmmPIDFDIdentityV1(final.process)
	if err != nil {
		return err
	}
	if record.PID != uint64(namespace[len(namespace)-1]) {
		return vmmModeFailureV1("the armed record differs from the original final creation")
	}
	if err := source.CheckArmedRecordV1(ctx, record); err != nil {
		return err
	}
	var trailing [1]byte
	n, err := reader.Read(trailing[:])
	if n != 0 || err != io.EOF {
		return errors.Join(err, vmmModeFailureV1("the final exec sentinel has trailing data or no actual close"))
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	for _, actor := range owner.actors {
		if actor == final || actor == owner.runner || actor.terminal {
			continue
		}
		ready, err := ownedPIDFDReady(int(actor.process.Fd()), 0)
		if err != nil || !ready {
			return errors.Join(err, vmmModeFailureV1("an original creator still has an unknown future exec path"))
		}
	}
	// The original runner closed its only remaining roles after the host's
	// continuation. The final fixed helper emitted 111, then made these roles
	// CLOEXEC immediately before its sole external exec; failure ends that task.
	if err := final.checkExecutableV1(program); err != nil {
		return err
	}
	if err := final.checkNamespacesV1(namespaces); err != nil {
		return err
	}
	owner.finalProved = true
	return nil
}

func vmmReadContextV1(ctx context.Context, file *os.File) (func(), error) {
	if file == nil {
		return nil, vmmModeFailureV1("the original final sentinel reader is missing")
	}
	deadline, _ := ctx.Deadline()
	if err := file.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { file.SetReadDeadline(time.Now()); close(finished) })
	return func() {
		if !stop() {
			<-finished
		}
		file.SetReadDeadline(time.Time{})
	}, nil
}
