//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// managerStoppedV1 contains an UNCONSUMED live capture, never an ordinary
// StoppedSession fact. Its distinct native manager settlement retains the same
// original holder OFD and the actual deposited kernel bundle across requests.
type managerStoppedV1 struct {
	mu                        sync.Mutex
	capture                   *StoppedSessionV1
	roles                     map[string]*os.File
	stopSent, settled, closed bool
}

func stopIntoManagerCustodyV1(ctx context.Context, operation *SessionOperation) (_ *managerStoppedV1, available bool, failure error) {
	capture, err := captureSupportedSupervisorV1(ctx, operation)
	if err != nil {
		return nil, false, err
	}
	if capture.invocation.runtime == nil {
		return nil, false, capture.Close()
	}
	stopped := &managerStoppedV1{capture: capture, roles: make(map[string]*os.File, custodyBundleSizeV1)}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, stopped.Close())
		}
	}()
	if err := stopped.captureRolesV1(ctx); err != nil {
		return nil, true, err
	}
	if err := stopped.checkLiveRolesV1(ctx); err != nil {
		return nil, true, err
	}
	args := proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: capture.state.Nonce}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	stopped.stopSent = true
	var accepted proto.NativeStopStateV1
	err = capture.nativeStopRequestV1(ctx, 2, proto.ManagerCustodyStopCommitV1, args, &accepted)
	if err == nil && accepted != capture.state {
		err = auxiliaryUnavailableV1("the original main accepted another manager stop continuation")
	}
	if settleErr := stopped.settleV1(); settleErr != nil {
		return nil, true, errors.Join(err, settleErr)
	}
	complete = true
	return stopped, true, err
}

func (stopped *managerStoppedV1) settleV1() error {
	if stopped == nil || !stopped.stopSent || stopped.capture == nil {
		return auxiliaryUnavailableV1("manager settlement has no original accepted live source")
	}
	if stopped.settled {
		return nil
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := stopped.checkNativeV1(ctx)
		cancel()
		if err == nil {
			stopped.settled = true
			return nil
		}
		// The actual original main and OP remain owned. A signal, timeout or
		// failed notification cannot manufacture processless manager custody.
		time.Sleep(25 * time.Millisecond)
	}
}

func (stopped *managerStoppedV1) Check(ctx context.Context, operation *SessionOperation) error {
	if stopped == nil || stopped.capture == nil || ctx == nil {
		return auxiliaryUnavailableV1("the original manager-stopped source is absent")
	}
	stopped.mu.Lock()
	defer stopped.mu.Unlock()
	owner := stopped.capture
	if stopped.closed || !stopped.settled || !stopped.stopSent || operation == nil || operation != owner.operation || !operation.Valid(operation.Primary()) || operation.native == nil || operation.native.borrowed || BootID() != owner.boot {
		return auxiliaryUnavailableV1("the manager-stopped source ended or changed operation")
	}
	if err := errors.Join(CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root)); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(current.VMNeedV1, owner.source.VMNeedV1) || !reflect.DeepEqual(current.RuntimeManifest, owner.source.RuntimeManifest) || current.Auxiliary != nil {
		return auxiliaryUnavailableV1("the original manager-stopped protected selection changed")
	}
	return stopped.checkNativeV1(ctx)
}

func (stopped *managerStoppedV1) Close() error {
	if stopped == nil {
		return nil
	}
	stopped.mu.Lock()
	defer stopped.mu.Unlock()
	if stopped.closed {
		return nil
	}
	if stopped.stopSent && !stopped.settled {
		if err := stopped.settleV1(); err != nil {
			return err
		}
	}
	stopped.closed = true
	var err error
	for role, file := range stopped.roles {
		err = errors.Join(err, file.Close())
		delete(stopped.roles, role)
	}
	// capture.stopSent remains false: its old ordinary settler is never used.
	return errors.Join(err, stopped.capture.Close())
}
