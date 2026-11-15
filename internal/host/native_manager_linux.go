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
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

const systemdManagerInterface = "org.freedesktop.systemd1.Manager"
const systemdUnitInterface = "org.freedesktop.systemd1.Unit"
const systemdServiceInterface = "org.freedesktop.systemd1.Service"
const systemdManagerPath = dbus.ObjectPath("/org/freedesktop/systemd1")

// nativeUserManagerV1 owns the direct native manager peer. It never resolves
// an ambient session bus, broker name, environment address or CLI subprocess.
type nativeUserManagerV1 struct {
	ctx        context.Context
	cancel     context.CancelFunc
	root       *os.Root
	rootPath   string
	rootRef    cachefs.Reference
	socketRef  os.FileInfo
	connection *net.UnixConn
	peer       *nativeStartupPeerV1
	bus        *dbus.Conn
	events     managerStartupEventsV1
	once       sync.Once
	closeErr   error
}

func openNativeUserManagerV1(ctx context.Context) (_ *nativeUserManagerV1, failure error) {
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
			failure = errors.Join(failure, manager.Close())
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
	stat, ok := manager.socketRef.Sys().(*unix.Stat_t)
	if !ok || manager.socketRef.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) {
		return nil, sessionLeaseError("the original native user manager socket is not private")
	}
	// Resolve through the held runtime directory descriptor. A renamed or
	// replaced ambient /run/user path cannot select another manager here.
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	dialPath := fmt.Sprintf("/proc/self/fd/%d/systemd/private", directory.Fd())
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", dialPath)
	err = errors.Join(err, directory.Close())
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	manager.connection = conn.(*net.UnixConn)
	manager.peer, err = pinOutsideStartupPeer(conn)
	if err != nil {
		return nil, err
	}
	if err := manager.checkNativeManager(); err != nil {
		return nil, err
	}
	manager.events.wake = make(chan struct{}, 1)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = conn.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	manager.bus, err = dbus.NewConn(conn, dbus.WithContext(lifetime), dbus.WithIncomingInterceptor(manager.events.observe))
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
	if err := manager.checkNativeManager(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return manager, nil
}

func (manager *nativeUserManagerV1) checkNativeManager() error {
	if manager == nil || manager.ctx == nil || manager.ctx.Err() != nil || manager.root == nil || manager.peer == nil {
		return sessionLeaseError("the original native user manager ended")
	}
	if err := manager.peer.Check(); err != nil {
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
	actual, err := startupExecutableInfo(manager.peer.proc)
	if err != nil {
		return err
	}
	trusted := false
	for _, path := range []string{"/usr/lib/systemd/systemd", "/lib/systemd/systemd"} {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*unix.Stat_t)
		if ok && stat.Uid == 0 && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0 && os.SameFile(actual, info) {
			trusted = true
			break
		}
	}
	if !trusted {
		return sessionLeaseError("the native manager peer is not the root-owned systemd executable")
	}
	command, err := manager.peer.proc.ReadFile("cmdline")
	args := strings.Split(strings.TrimSuffix(string(command), "\x00"), "\x00")
	if err != nil || len(args) != 2 || args[1] != "--user" {
		return errors.Join(sessionLeaseError("the native manager peer is not the user's manager process"), err)
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
	return manager.peer.Check()
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
	manager.once.Do(func() {
		manager.cancel()
		if manager.bus != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.bus.Close())
		} else if manager.connection != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.connection.Close())
		}
		if manager.peer != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.peer.Close())
		}
		if manager.root != nil {
			manager.closeErr = errors.Join(manager.closeErr, manager.root.Close())
		}
	})
	return manager.closeErr
}
