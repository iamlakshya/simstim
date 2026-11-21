//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

type custodyRuntimeV1 struct {
	mu                                  sync.Mutex
	service                             *custodyConfigFileV1
	anchor                              *custodyConfigFileV1
	pin                                 *custodyConfigFileV1
	pins                                *os.Root
	root                                *os.Root
	pinRemoved, serviceRemoved, retired bool
	retiredFiles                        []*custodyRetiredConfigV1
	installManager                      *nativeUserManagerV1
	pendingInstall                      bool
	retirement                          nativeStartupRetirementsV1
	diagnostic                          error
}

func (manager *nativeUserManagerV1) supportsCustodyV1(ctx context.Context) (bool, error) {
	version, err := manager.getProperty(ctx, systemdManagerPath, systemdManagerInterface, "Version")
	if err != nil {
		return false, err
	}
	value, ok := version.Value().(string)
	if !ok {
		return false, sessionLeaseError("the native manager has no known custody version")
	}
	value = strings.TrimPrefix(value, "systemd ")
	n := strings.IndexFunc(value, func(c rune) bool { return c < '0' || c > '9' })
	if n >= 0 {
		value = value[:n]
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return false, errors.Join(sessionLeaseError("the native manager custody version is unknown"), err)
	}
	// Preserve=yes was added in 254. Older managers retain their original
	// live-only path and never originate a same-boot custody source.
	return number >= 254, nil
}

func (manager *nativeUserManagerV1) installCustodyRuntimeV1(ctx context.Context, m *Manifest, executable string, argv []string, admission *runtimePublication) (result *custodyRuntimeV1, failure error) {
	if admission == nil || admission.startup == nil || admission.startup.native == nil || admission.startup.native.borrowed || !admission.startup.Valid(m.ID) {
		return nil, sessionLeaseError("runtime custody creation has no original outside operation")
	}
	content, err := custodyServiceFileV1(m, executable, argv)
	if err != nil {
		return nil, err
	}
	config, err := manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, config.Close()) }()
	if err := errors.Join(admission.startup.native.check(ctx), manager.requireAuxiliaryUnitAbsentV1(ctx, UnitName(m.ID))); err != nil {
		return nil, err
	}
	// The service must be genuinely new. An existing service file or loaded
	// unit is never reinterpreted as an original creation source.
	runtime := &custodyRuntimeV1{installManager: manager, pendingInstall: true}
	complete := false
	defer func() {
		if !complete {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			runtime.mu.Lock()
			cleanup := runtime.cleanupPendingInstallWithConfigLockedV1(cleanupContext, config)
			runtime.mu.Unlock()
			cancel()
			if cleanup == nil {
				cleanup = runtime.Close()
			}
			failure = errors.Join(failure, cleanup, runtime.CloseDiagnosticsV4())
			if cleanup != nil {
				result = runtime
			}
		}
	}()
	// Every partial creation owns a separate retained root before effects.
	// Closing the serialisation scope cannot strand its cleanup descriptors.
	runtime.root, err = config.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	runtime.anchor, err = ensureCustodyConfigFileV1(runtime.root, custodyTargetV1(), custodyTargetFileV1())
	if err != nil {
		return nil, err
	}
	runtime.service, err = openCustodyConfigFileV1(runtime.root, UnitName(m.ID), content, true)
	if err != nil {
		return nil, err
	}
	runtime.pins, err = cachefs.OpenPrivateChild(runtime.root, custodyTargetV1()+".d", true)
	if err != nil {
		return nil, err
	}
	name, pin, err := custodyPinFileV1(m.ID)
	if err != nil {
		return nil, err
	}
	runtime.pin, err = openCustodyConfigFileV1(runtime.pins, name, pin, true)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(syncCapturedDirectory(runtime.root), syncCapturedDirectory(runtime.pins)); err != nil {
		return nil, err
	}
	if err := config.reload(ctx); err != nil {
		return nil, err
	}
	if err := manager.startCustodyTargetV1(ctx); err != nil {
		return nil, err
	}
	if err := manager.checkCustodyPinV1(ctx, UnitName(m.ID)); err != nil {
		return nil, err
	}
	if err := errors.Join(runtime.service.check(), runtime.pin.check(), config.check(ctx), admission.startup.native.check(ctx)); err != nil {
		return nil, err
	}
	complete = true
	return runtime, nil
}

func (manager *nativeUserManagerV1) startCustodyTargetV1(ctx context.Context) error {
	var job dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".StartUnit", 0, custodyTargetV1(), "fail").Store(&job); err != nil {
		return err
	}
	if !job.IsValid() || job == "/" {
		return sessionLeaseError("the passive custody anchor has no original start job")
	}
	for {
		var path dbus.ObjectPath
		err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, custodyTargetV1()).Store(&path)
		if err != nil {
			return err
		}
		value, err := manager.getProperty(ctx, path, systemdUnitInterface, "ActiveState")
		if err != nil {
			return err
		}
		if active, ok := value.Value().(string); ok && active == "active" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (runtime *custodyRuntimeV1) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.pendingInstall {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := runtime.cleanupPendingInstallLockedV1(cleanup)
		cancel()
		if err != nil {
			return err
		}
	}
	for _, retired := range runtime.retiredFiles {
		closed := retired.closeV1()
		if retired != nil && retired.closeFacts != nil && retired.closeFacts.Retired && retired.closeFacts.Unknown == nil {
			runtime.diagnostic = errors.Join(runtime.diagnostic, retired.closeFacts.Diagnostic)
		} else if closed != nil {
			return closed
		}
	}
	runtime.retiredFiles = nil
	for _, file := range []*custodyConfigFileV1{runtime.service, runtime.pin, runtime.anchor} {
		if file == nil {
			continue
		}
		closed := file.closeDescriptorV1()
		if file.closeFacts != nil && file.closeFacts.Retired && file.closeFacts.Unknown == nil {
			runtime.diagnostic = errors.Join(runtime.diagnostic, file.closeFacts.Diagnostic)
		} else if closed != nil {
			return closed
		}
	}
	roots := []*os.Root{runtime.pins, runtime.root}
	_ = runtime.retirement.closeRootsV4(roots)
	if unknown := runtime.retirement.checkRootsV4(roots); unknown != nil {
		return unknown
	}
	return nil
}

// Report data is retained independently from native cleanup progress.
func (runtime *custodyRuntimeV1) CloseDiagnosticsV4() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return errors.Join(runtime.diagnostic, runtime.retirement.diagnosticsV4())
}
