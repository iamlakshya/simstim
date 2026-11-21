//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// Scope custody is lifetime only. This SAME original factory/Cmd owns its
// manager dispatch, unreaped child, first job, native group and membership.
// No incoming PID/descriptor/property/report can construct this producer.
type purgeAttemptScopeV3 struct {
	mu                                  sync.Mutex
	owner                               *purgeAttemptExecV1
	manager                             *nativeUserManagerV1
	unit, groupPath                     string
	adoption                            *os.File
	group                               *os.File
	groupInfo                           os.FileInfo
	closure                             *purgeAttemptScopeClosureV3
	unstarted                           *purgeAttemptScopeUnstartedCloseV3
	invocation                          [16]byte
	events                              purgeAttemptScopeEventsV3
	signals                             chan *dbus.Signal
	watchDone                           chan struct{}
	call                                *dbus.Call
	reply                               *dbus.Call
	job                                 *purgeAttemptScopeJobV3
	stopCall                            *dbus.Call
	stopReply                           *dbus.Call
	stopJob                             *purgeAttemptScopeJobV3
	pidfds                              bool
	negative, adopted, notified, closed bool
	dispatched                          bool
}

func newPurgeAttemptScopeV3(ctx context.Context, owner *purgeAttemptExecV1) (result *purgeAttemptScopeV3, failure error) {
	if ctx == nil || owner == nil || owner.start == nil || owner.command != owner.start.command || owner.process != nil || owner.scope != nil || owner.reap == nil {
		return nil, placementError("the purge scope has no same original unstarted fixed Cmd")
	}
	manager, err := openNativeUserManagerV1(ctx)
	if err != nil {
		return nil, errors.Join(UserError("purge_scope", "the protected removal has no current native user manager", "Keep the host saved. Prepare its user manager and resume the same removal."), err)
	}
	scope := &purgeAttemptScopeV3{owner: owner, manager: manager, signals: make(chan *dbus.Signal, 256)}
	complete := false
	defer func() {
		if !complete {
			closeErr := scope.closeUnstartedV3()
			failure = errors.Join(failure, closeErr)
			if scope.checkNativeRetiredV3(context.Background()) != nil {
				result = scope
			}
		}
	}()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	scope.unit = brand.Name + "-purge-" + hex.EncodeToString(nonce[:]) + ".scope"
	scope.groupPath = fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service/app.slice/%s", os.Getuid(), os.Getuid(), scope.unit)
	scope.events = purgeAttemptScopeEventsV3{unit: scope.unit, jobs: map[dbus.ObjectPath]*purgeAttemptScopeJobV3{}, wake: make(chan struct{}, 1)}
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, err
	}
	scope.adoption = os.NewFile(uintptr(fd), "actual-purge-scope-adoption-completion")
	if err := scope.requireAbsentV3(ctx); err != nil {
		return nil, err
	}
	value, err := manager.getProperty(ctx, systemdManagerPath, systemdManagerInterface, "Version")
	if err != nil {
		return nil, err
	}
	version, ok := value.Value().(string)
	if !ok {
		return nil, placementError("the actual user manager has no native version observation")
	}
	fields := strings.Fields(version)
	if len(fields) == 0 {
		return nil, placementError("the actual user manager version is empty")
	}
	major := strings.SplitN(fields[0], ".", 2)[0]
	n, err := strconv.ParseUint(major, 10, 32)
	if err != nil {
		return nil, placementError("the actual user manager version is unsupported")
	}
	// Tagged v255/v257 parse PIDFDs as ah. Downstream attachment is still
	// numeric; this branch ALWAYS retains the original unreaped reservation.
	scope.pidfds = n >= 255 && manager.bus.SupportsUnixFDs()
	manager.bus.Signal(scope.signals)
	scope.watchDone = make(chan struct{})
	go func() {
		defer close(scope.watchDone)
		for {
			select {
			case <-manager.ctx.Done():
				return
			case signal, ok := <-scope.signals:
				if !ok {
					return
				}
				scope.events.observeV3(signal)
			}
		}
	}()
	complete = true
	return scope, nil
}

func (scope *purgeAttemptScopeV3) adoptOriginalV3() error {
	owner := scope.owner
	if owner == nil || owner.scope != scope || owner.start == nil || owner.reap == nil || owner.process == nil || owner.creation == nil {
		return placementError("the original scope has no same actual child to adopt")
	}
	if scope.dispatched || scope.call != nil || scope.reply != nil || scope.job != nil {
		return placementError("the SAME original scope has already crossed its only adoption dispatch")
	}
	ctx := scope.manager.ctx
	if err := errors.Join(scope.manager.checkNativeManager(), owner.reap.checkHeldV3(ctx), owner.start.checkFrozenV2(ctx)); err != nil {
		return err
	}
	property := func(name string, value any) transientPropertyV1 {
		return transientPropertyV1{name, dbus.MakeVariant(value)}
	}
	properties := []transientPropertyV1{property("Description", brand.DisplayName+" protected removal"), property("Slice", "app.slice"), property("CollectMode", "inactive-or-failed"), property("KillMode", "control-group"), property("TimeoutStopUSec", uint64(30_000_000))}
	if scope.pidfds {
		properties = append(properties, property("PIDFDs", []dbus.UnixFD{dbus.UnixFD(owner.creation.Fd())}))
	} else {
		properties = append(properties, property("PIDs", []uint32{uint32(owner.process.Pid)}))
	}
	// No request cancellation controls the real dispatch lifetime. The caller
	// cannot return/EndBridge cannot join until this SAME transaction settles.
	scope.dispatched = true
	scope.call = scope.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(ctx, systemdManagerInterface+".StartTransientUnit", 0, make(chan *dbus.Call, 1), scope.unit, "fail", properties, []transientAuxV1{})
	scope.reply = <-scope.call.Done
	var path dbus.ObjectPath
	if err := scope.call.Store(&path); err != nil {
		if scope.knownNegativeV3(err) == nil {
			scope.negative = true
			if err := scope.finishAdoptionV3(); err != nil {
				return err
			}
			return errors.Join(placementError("the actual user manager refused the original removal scope"), err)
		}
		return scope.retainUnknownV3(err)
	}
	job, err := scope.events.waitOriginalJobV3(ctx, path)
	if err != nil {
		return scope.retainUnknownV3(err)
	}
	scope.job = job
	if err := owner.reap.checkHeldV3(ctx); err != nil {
		return scope.retainUnknownV3(err)
	}
	if err := scope.captureOriginalUnitV3(ctx); err != nil {
		if nativePurgeCreationExitedV3(owner) == nil && scope.requireAbsentV3(ctx) == nil {
			if err := scope.finishAdoptionV3(); err != nil {
				return err
			}
			return placementError("the actual original removal child exited before manager adoption")
		}
		return scope.retainUnknownV3(err)
	}
	if job.result != "done" || nativePurgeCreationExitedV3(owner) == nil {
		if err := scope.finishAdoptionV3(); err != nil {
			return err
		}
		return placementError("the original removal scope did not adopt a live child")
	}
	if err := errors.Join(scope.checkActiveV3(ctx), scope.checkMembershipV3(ctx)); err != nil {
		return scope.retainUnknownV3(err)
	}
	scope.adopted = true
	return scope.finishAdoptionV3()
}

func (scope *purgeAttemptScopeV3) finishAdoptionV3() error {
	if err := scope.checkTerminalForReapV3(); err != nil {
		return err
	}
	if !scope.notified {
		if err := ordinaryJoinLatchV1(scope.adoption, false); err != nil {
			return err
		}
		var value [8]byte
		binary.NativeEndian.PutUint64(value[:], 1)
		n, err := scope.adoption.Write(value[:])
		if err != nil || n != len(value) {
			return errors.Join(placementError("the original child received no exact scope settlement notification"), err)
		}
		scope.notified = true
	}
	return scope.owner.reap.releaseAfterScopeV3(scope)
}

func (scope *purgeAttemptScopeV3) checkTerminalForReapV3() error {
	if scope == nil || scope.owner == nil || scope.owner.scope != scope || scope.owner.start == nil || scope.owner.start.scope != scope || scope.owner.start.creator != scope.owner || scope.owner.reap == nil || scope.owner.reap.owner != scope.owner || scope.owner.start.command != scope.owner.command || scope.manager == nil {
		return placementError("the reaping release has no same actual scope dispatch")
	}
	if err := scope.owner.start.checkRetainedSpecV3(context.Background()); err != nil {
		return err
	}
	if !scope.dispatched && scope.call == nil && scope.reply == nil && scope.job == nil {
		return nil
	}
	if scope.call == nil || scope.reply != scope.call || scope.call.Destination != "org.freedesktop.systemd1" || scope.call.Path != systemdManagerPath || scope.call.Method != systemdManagerInterface+".StartTransientUnit" || scope.call.ResponseSequence == dbus.NoSequence {
		return placementError("the original scope call has not settled")
	}
	if err := scope.manager.checkNativeManager(); err != nil {
		return err
	}
	if scope.negative {
		return scope.knownNegativeV3(scope.call.Err)
	}
	if err := scope.events.checkOriginalJobV3(scope.job); err != nil {
		return err
	}
	var path dbus.ObjectPath
	if err := scope.call.Store(&path); err != nil || path != scope.job.path {
		return errors.Join(placementError("the original scope job differs from its actual call reply"), err)
	}
	return nil
}

func (scope *purgeAttemptScopeV3) knownNegativeV3(failure error) error {
	if scope.call == nil || scope.reply != scope.call || scope.call.ResponseSequence == dbus.NoSequence {
		return placementError("the refused scope has no same actual native manager reply")
	}
	switch failure.(type) {
	case dbus.Error, *dbus.Error:
	default:
		return placementError("the original scope call has unknown native dispatch")
	}
	scope.events.mu.Lock()
	noJobs := len(scope.events.jobs) == 0
	eventErr := scope.events.err
	scope.events.mu.Unlock()
	if !noJobs || eventErr != nil {
		return errors.Join(placementError("the refused original scope still has unknown job custody"), eventErr)
	}
	return scope.requireAbsentV3(scope.manager.ctx)
}

func (scope *purgeAttemptScopeV3) retainUnknownV3(failure error) error {
	// This exact original stack retains the unreaped child and real manager
	// transaction. Reconnecting, cancellation and elapsed time create no proof.
	_ = failure
	select {}
}

func (scope *purgeAttemptScopeV3) requireAbsentV3(ctx context.Context) error {
	if err := scope.manager.checkNativeManager(); err != nil {
		return err
	}
	var path dbus.ObjectPath
	call := scope.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, scope.unit)
	if err := call.Store(&path); err != nil {
		var remote dbus.Error
		var pointer *dbus.Error
		if errors.As(err, &remote) && remote.Name == "org.freedesktop.systemd1.NoSuchUnit" || errors.As(err, &pointer) && pointer != nil && pointer.Name == "org.freedesktop.systemd1.NoSuchUnit" {
			return scope.manager.checkNativeManager()
		}
		return err
	}
	return placementError("the exact original purge scope still exists or was replaced")
}

func (scope *purgeAttemptScopeV3) finishUnsubmittedV3() error {
	if scope == nil || scope.owner == nil || scope.owner.scope != scope || scope.dispatched || scope.call != nil || scope.reply != nil || scope.job != nil {
		return placementError("the original manager adoption remains dispatched or unknown")
	}
	return scope.finishAdoptionV3()
}
