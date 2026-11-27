//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// This is a CURRENT live invocation captured from a socket and a fresh native
// response. It is not a reconstructed original creation job or saved PID.
type nativeStopInvocationV1 struct {
	unmanagedNoneWarm bool // only the actual live none-resume capture issues this
	manager           *nativeUserManagerV1
	peer              *nativeStartupPeerV1
	path              dbus.ObjectPath
	properties        unitActivationProperties
	unit, primary     string
	executable        os.FileInfo
	argv              []string
	cgroup            *os.File
	memoryMax         int64
	runtime           *custodyRuntimeV1
}

func captureNativeStopInvocationV1(ctx context.Context, manager *nativeUserManagerV1, connection net.Conn, primary string, executable os.FileInfo, maximum int64) (_ *nativeStopInvocationV1, returned error) {
	actual, err := peerOf(connection)
	if err != nil {
		return nil, err
	}
	if actual.done != nil {
		defer actual.done()
	}
	if actual.pid <= 0 || actual.uid != os.Getuid() {
		return nil, auxiliaryUnavailableV1("the live supervisor socket has another native account")
	}
	pidfd, err := unix.PidfdOpen(actual.pid, 0)
	if err != nil {
		return nil, err
	}
	process, err := os.OpenRoot(fmt.Sprintf("/proc/%d", actual.pid))
	if err != nil {
		return nil, errors.Join(err, unix.Close(pidfd))
	}
	peer := &nativeStartupPeerV1{pid: actual.pid, uid: uint32(actual.uid), pidfd: pidfd, proc: process}
	complete := false
	result := &nativeStopInvocationV1{manager: manager, peer: peer, unit: UnitName(primary), primary: primary, executable: executable, memoryMax: maximum}
	defer func() {
		if !complete {
			returned = errors.Join(returned, result.Close())
		}
	}()
	if actual.alive != nil && !actual.alive() {
		return nil, auxiliaryUnavailableV1("the actual supervisor socket creator already ended")
	}
	if err := peer.checkLive(); err != nil {
		return nil, err
	}
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(peer.pid)).Store(&result.path); err != nil {
		return nil, err
	}
	result.properties, err = manager.activation(ctx, result.path, result.unit)
	if err != nil {
		return nil, err
	}
	if result.properties.MainPID != uint32(peer.pid) || result.properties.Type != "exec" {
		return nil, auxiliaryUnavailableV1("the actual supervisor is not its exact supported native exec invocation")
	}
	command, err := process.ReadFile("cmdline")
	if err != nil {
		return nil, err
	}
	argv := strings.Split(strings.TrimSuffix(string(command), "\x00"), "\x00")
	if len(argv) != 3 || argv[2] != primary || argv[1] != "supervise" && argv[1] != "supervise-restore-v1" {
		return nil, auxiliaryUnavailableV1("the current native supervisor has another closed command")
	}
	result.argv = argv
	result.cgroup, err = openNativeStopCgroupV1(primary)
	if err != nil {
		return nil, err
	}
	if err := result.checkCreationV1(ctx); err != nil {
		return nil, err
	}
	result.runtime, err = manager.captureCustodyRuntimeV1(ctx, result.path, primary)
	if err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func (invocation *nativeStopInvocationV1) Check(ctx context.Context) error {
	return invocation.checkInvocationV1(ctx, true)
}
func (invocation *nativeStopInvocationV1) checkCreationV1(ctx context.Context) error {
	return invocation.checkInvocationV1(ctx, false)
}
func (invocation *nativeStopInvocationV1) checkInvocationV1(ctx context.Context, identity bool) error {
	if invocation != nil && invocation.unmanagedNoneWarm {
		return invocation.checkNoneWarmV1(ctx, identity)
	}
	if invocation == nil || ctx == nil || invocation.peer == nil || invocation.cgroup == nil || invocation.manager == nil {
		return auxiliaryUnavailableV1("the actual supported supervisor invocation ended")
	}
	if err := errors.Join(invocation.peer.checkLive(), invocation.manager.checkNativeManager(), ctx.Err()); err != nil {
		return err
	}
	var path dbus.ObjectPath
	if err := invocation.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(invocation.peer.pid)).Store(&path); err != nil {
		return err
	}
	current, err := invocation.manager.activation(ctx, path, invocation.unit)
	if err != nil {
		return err
	}
	if path != invocation.path || !sameUnitActivationProperties(current, invocation.properties) {
		return auxiliaryUnavailableV1("the actual native stop invocation was replaced")
	}
	if identity {
		if err := checkNativeHelperExecutableV1(invocation.peer, invocation.executable, invocation.argv); err != nil {
			return err
		}
	} else if err := checkCreatedHelperArgumentsV1(invocation.peer, invocation.argv); err != nil {
		return err
	}
	data, err := invocation.peer.proc.ReadFile("cgroup")
	if err != nil {
		return err
	}
	expected := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", os.Getuid(), os.Getuid(), brand.Slice, invocation.unit)
	if !bytes.Equal(data, []byte(expected)) {
		return auxiliaryUnavailableV1("the current native supervisor is outside its exact original unit")
	}
	native, err := readMemoryUnitV1(ctx, invocation.primary, true)
	if err != nil {
		return err
	}
	if !native.exists || native.maximum != invocation.memoryMax || native.swap != 0 {
		return auxiliaryUnavailableV1("the current native supervisor lost its exact declared unit boundary")
	}
	fresh, err := openNativeStopCgroupV1(invocation.primary)
	if err != nil {
		return err
	}
	original, statErr := invocation.cgroup.Stat()
	actual, otherErr := fresh.Stat()
	closeErr := fresh.Close()
	if statErr != nil || otherErr != nil || !os.SameFile(original, actual) {
		return errors.Join(auxiliaryUnavailableV1("the original native supervisor cgroup object changed"), statErr, otherErr, closeErr)
	}
	return errors.Join(closeErr, invocation.peer.checkLive(), invocation.manager.checkNativeManager(), ctx.Err())
}
func (invocation *nativeStopInvocationV1) Close() error {
	if invocation == nil {
		return nil
	}
	var failure error
	if invocation.cgroup != nil {
		failure = errors.Join(failure, invocation.cgroup.Close())
	}
	if invocation.peer != nil {
		failure = errors.Join(failure, invocation.peer.Close())
	}
	return errors.Join(failure, invocation.runtime.Close())
}
func openNativeStopCgroupV1(primary string) (*os.File, error) {
	if !validID(primary) {
		return nil, auxiliaryUnavailableV1("the native stop has no canonical primary")
	}
	root, err := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(root, &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errors.Join(auxiliaryUnavailableV1("the native stop root is not cgroup v2"), err)
	}
	name := fmt.Sprintf("user.slice/user-%d.slice/user@%d.service/%s/%s", os.Getuid(), os.Getuid(), brand.Slice, UnitName(primary))
	descriptor, err := unix.Openat2(root, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), "native-stopped-session-cgroup"), nil
}
