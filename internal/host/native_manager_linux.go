//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

const systemdManagerInterface = "org.freedesktop.systemd1.Manager"
const systemdUnitInterface = "org.freedesktop.systemd1.Unit"
const systemdServiceInterface = "org.freedesktop.systemd1.Service"
const systemdManagerPath = dbus.ObjectPath("/org/freedesktop/systemd1")

// nativeUserManagerV1 owns the direct native manager peer. It never resolves
// an ambient session bus, broker name, environment address or CLI subprocess.
type nativeUserManagerV1 struct {
	ctx               context.Context
	cancel            context.CancelFunc
	root              *os.Root
	rootPath          string
	rootRef           cachefs.Reference
	socketRef         os.FileInfo
	connection        *nativeManagerTransportV1
	retirement        nativeStartupRetirementsV1
	peer              *nativeStartupPeerV1
	bus               *dbus.Conn
	events            managerStartupEventsV1
	once              sync.Once
	closeMu           sync.Mutex
	resourceMu        sync.RWMutex
	closeErr          error
	setupComplete     bool
	directory         *os.File
	directoryOriginal *os.File
	directoryClose    *supply.FileCloseFactsV1
}

func openNativeUserManagerV1(ctx context.Context) (result *nativeUserManagerV1, failure error) {
	if ctx == nil {
		return nil, sessionLeaseError("the native manager has no original owner lifetime")
	}
	path := fmt.Sprintf("/run/user/%d", os.Getuid())
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	manager := &nativeUserManagerV1{ctx: lifetime, cancel: cancel, root: root, rootPath: path}
	defer func() {
		if failure != nil {
			diagnostic, unknown := retireNativeManagerV4(context.Background(), manager)
			failure = errors.Join(failure, diagnostic, unknown)
			if unknown != nil {
				result = manager
			} else {
				// The failed setup and its first I/O diagnostic remain errors,
				// but every original resource has positive native retirement.
				result = nil
			}
		}
	}()
	manager.rootRef, err = cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	manager.socketRef, err = root.Lstat("systemd/private")
	if err != nil {
		return nil, err
	}
	if !nativeManagerSocketOwnerV1(manager.socketRef) {
		return nil, sessionLeaseError("the original native user manager socket is not private")
	}
	// Resolve through the held runtime directory descriptor. A renamed or
	// replaced ambient /run/user path cannot select another manager here.
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	manager.directory, manager.directoryOriginal = directory, directory
	dialPath := fmt.Sprintf("/proc/self/fd/%d/systemd/private", directory.Fd())
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", dialPath)
	if conn != nil {
		manager.connection = newNativeManagerTransportV1(conn.(*net.UnixConn), manager.retirement.descriptorOwnerV4())
	} // Own it before a late directory-close error.
	facts := supply.CloseFileOnceV1(directory)
	manager.directoryClose = &facts
	if facts.Retired {
		manager.directory = nil
	}
	err = errors.Join(err, facts.Diagnostic, facts.Unknown)
	if err != nil {
		return nil, err
	}
	err = manager.captureOriginalPeerV4()
	if err != nil {
		return nil, err
	}
	if err := manager.checkNativeManagerPeerV1(); err != nil {
		return nil, err
	}
	manager.events.wake = make(chan struct{}, 1)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = manager.connection.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = manager.connection.SetDeadline(deadline)
	}
	manager.bus, err = dbus.NewConn(manager.connection, dbus.WithContext(lifetime), dbus.WithIncomingInterceptor(manager.events.observe))
	if err != nil {
		return nil, err
	}
	if err := manager.bus.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}); err != nil {
		return nil, err
	}
	// This direct peer is systemd itself, not a bus daemon: it requires no
	// name-registration Hello. Subscribe stays owned on this exact connection.
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".Subscribe", 0).Err; err != nil {
		return nil, err
	}
	if err := manager.checkNativeManagerPeerV1(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = manager.connection.SetDeadline(time.Time{})
	manager.setupComplete = true
	return manager, nil
}

func (manager *nativeUserManagerV1) checkNativeManager() error {
	if manager == nil || !manager.setupComplete {
		return sessionLeaseError("the original manager setup is incomplete; its retained object is cleanup-only")
	}
	return manager.checkNativeManagerPeerV1()
}

func (manager *nativeUserManagerV1) checkNativeManagerPeerV1() error {
	if manager == nil {
		return sessionLeaseError("the original native user manager ended")
	}
	manager.resourceMu.RLock()
	defer manager.resourceMu.RUnlock()
	if manager == nil || manager.ctx == nil || manager.ctx.Err() != nil || manager.root == nil || manager.peer == nil || manager.connection == nil || manager.connection.isClosingV1() {
		return sessionLeaseError("the original native user manager ended")
	}
	if err := manager.checkManagerPeerNamespaceV1(); err != nil {
		return err
	}
	if err := manager.rootRef.CheckRoot(manager.root); err != nil {
		return err
	}
	fresh, err := cachefs.OpenPrivateRoot(manager.rootPath, false)
	if err != nil {
		return err
	}
	err = errors.Join(manager.rootRef.CheckRoot(fresh), fresh.Close())
	if err != nil {
		return err
	}
	socket, err := manager.root.Lstat("systemd/private")
	if err != nil || !os.SameFile(socket, manager.socketRef) {
		return errors.Join(sessionLeaseError("the original user manager socket changed"), err)
	}
	command, err := manager.peer.proc.ReadFile("cmdline")
	args := strings.Split(strings.TrimSuffix(string(command), "\x00"), "\x00")
	if err != nil || len(args) != 2 || args[1] != "--user" {
		return errors.Join(sessionLeaseError("the native manager peer is not the user's manager process"), err)
	}
	if err := manager.checkManagerExecutableV1(args[0]); err != nil {
		return err
	}
	cgroup, err := manager.peer.proc.ReadFile("cgroup")
	paths, _ := cgroupPaths(cgroup)
	expected := []string{"user.slice", fmt.Sprintf("user-%d.slice", os.Getuid()), fmt.Sprintf("user@%d.service", os.Getuid()), "init.scope"}
	correct := false
	for _, path := range paths {
		if strings.Join(path, "/") == "/"+strings.Join(expected, "/") {
			correct = true
		}
	}
	if err != nil || !correct {
		return errors.Join(sessionLeaseError("the native user manager is outside its original system user service"), err)
	}
	return manager.checkManagerPeerNamespaceV1()
}

// These are native file observations, not manager/source constructors. os
// returns syscall.Stat_t here; x/sys/unix.Stat_t is a different named type.
func nativeManagerSocketOwnerV1(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode()&os.ModeSocket != 0 && stat.Uid == uint32(os.Getuid())
}

func nativeManagerExecutableOwnerV1(actual, expected os.FileInfo) bool {
	if actual == nil || expected == nil {
		return false
	}
	stat, ok := expected.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && expected.Mode().IsRegular() && expected.Mode().Perm()&0o022 == 0 && os.SameFile(actual, expected)
}

func (manager *nativeUserManagerV1) getProperty(ctx context.Context, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	if err := manager.checkNativeManager(); err != nil {
		return dbus.Variant{}, err
	}
	var value dbus.Variant
	err := manager.bus.Object("org.freedesktop.systemd1", path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&value)
	return value, errors.Join(err, manager.checkNativeManager(), manager.events.failure())
}

func (manager *nativeUserManagerV1) activation(ctx context.Context, path dbus.ObjectPath, unit string) (unitActivationProperties, error) {
	values := map[string]dbus.Variant{}
	for _, item := range []struct{ iface, name string }{
		{systemdUnitInterface, "Id"}, {systemdUnitInterface, "ActiveState"}, {systemdUnitInterface, "InvocationID"},
		{systemdServiceInterface, "MainPID"}, {systemdServiceInterface, "ControlGroup"}, {systemdServiceInterface, "Type"},
		{systemdServiceInterface, "NRestarts"},
	} {
		value, err := manager.getProperty(ctx, path, item.iface, item.name)
		if err != nil {
			return unitActivationProperties{}, err
		}
		values[item.name] = value
	}
	id, idOK := values["Id"].Value().(string)
	active, activeOK := values["ActiveState"].Value().(string)
	invocation, invocationOK := values["InvocationID"].Value().([]byte)
	pid, pidOK := values["MainPID"].Value().(uint32)
	cgroup, cgroupOK := values["ControlGroup"].Value().(string)
	kind, kindOK := values["Type"].Value().(string)
	restarts, restartsOK := values["NRestarts"].Value().(uint32)
	if !idOK || id != unit || !activeOK || active != "active" || !invocationOK || len(invocation) != 16 ||
		!pidOK || !cgroupOK || !kindOK || kind != "exec" || !restartsOK || restarts != 0 || filepath.Base(cgroup) != unit {
		return unitActivationProperties{}, sessionLeaseError("the original manager did not report the first direct exec activation")
	}
	// Reuse the Root-owned strict data decoder. This observation remains
	// insufficient until the same launch's native process and peer are bound.
	raw := fmt.Sprintf("InvocationID=%s\nMainPID=%d\nControlGroup=%s\nType=%s\n", hex.EncodeToString(invocation), pid, cgroup, kind)
	return decodeUnitActivationProperties([]byte(raw))
}

func (manager *nativeUserManagerV1) Close() error {
	if manager == nil {
		return nil
	}
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.resourceMu.Lock()
	defer manager.resourceMu.Unlock()
	manager.once.Do(func() {
		// The wrapper was installed before NewConn. A library EOF or
		// cancellation therefore uses the SAME first-close resource owner.
		if manager.bus != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.bus.Close())
		} else if manager.connection != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.connection.Close())
		}
		if manager.cancel != nil {
			manager.cancel()
		}
		manager.closeErr = errors.Join(manager.closeErr, manager.retirement.closePeerV4(manager.peer))
		if manager.directory != nil && manager.directoryClose == nil {
			manager.directoryOriginal = manager.directory
			facts := supply.CloseFileOnceV1(manager.directory)
			manager.directoryClose = &facts
		}
		if manager.directoryClose != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.directoryClose.Diagnostic, nativeManagerDirectoryRetirementV1(manager))
			if manager.directoryClose.Retired && manager.directoryClose.Unknown == nil {
				manager.directory = nil
			}
		}
		manager.closeErr = errors.Join(manager.closeErr, manager.retirement.closeRootsV4([]*os.Root{manager.root}))
	})
	return errors.Join(manager.closeErr, manager.checkNativeRetiredV3(context.Background()))
}

// This is resource retirement of this SAME original manager object. It is
// never operational admission or permission to observe/start/stop a unit.
func (manager *nativeUserManagerV1) checkNativeRetiredV3(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return sessionLeaseError("the original manager native retirement is absent")
	}
	var transport error
	if manager.connection != nil {
		transport = manager.connection.checkNativeRetiredV1()
	}
	return errors.Join(ctx.Err(), transport, nativeManagerDirectoryRetirementV1(manager), manager.retirement.checkPeerV4(manager.peer), manager.retirement.checkRootsV4([]*os.Root{manager.root}), manager.retirement.checkAllRetiredV4())
}

func (manager *nativeUserManagerV1) CloseDiagnosticsV3() error {
	if manager == nil {
		return nil
	}
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	var directory error
	if manager.directoryClose != nil {
		directory = manager.directoryClose.Diagnostic
	}
	return errors.Join(manager.closeErr, directory, manager.retirement.diagnosticsV4())
}
