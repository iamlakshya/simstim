//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
)

type transientPropertyV1 struct {
	Name  string
	Value dbus.Variant
}

type transientExecV1 struct {
	Path  string
	Argv  []string
	Flags []string
}

type transientAuxV1 struct {
	Name       string
	Properties []transientPropertyV1
}

type nativeUnitActivationV1 struct {
	manager    *nativeUserManagerV1
	path       dbus.ObjectPath
	unit       string
	properties unitActivationProperties
	peer       *nativeStartupPeerV1
	executable os.FileInfo
	argv       []string
	memoryMax  int64
	once       sync.Once
	closeErr   error
}

func (manager *nativeUserManagerV1) startSupervisor(ctx context.Context, m *Manifest, executable string, argv []string, admission *runtimePublication) (*nativeUnitActivationV1, error) {
	unit := UnitName(m.ID)
	if err := errors.Join(manager.checkNativeManager(), manager.events.arm(unit)); err != nil {
		return nil, err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return nil, err
	}
	property := func(name string, value any) transientPropertyV1 {
		return transientPropertyV1{name, dbus.MakeVariant(value)}
	}
	properties := []transientPropertyV1{
		property("Description", brand.Name+" session "+m.ID),
		property("Type", "exec"), property("Slice", brand.Slice),
		property("KillMode", "mixed"), property("TimeoutStopUSec", uint64(30_000_000)),
		property("OOMPolicy", "continue"), property("Restart", "no"),
		property("CollectMode", "inactive-or-failed"), property("TasksMax", uint64(4096)),
		// ExecStartEx/no-env-expand is supported by systemd 252's actual
		// transient command parser. Both the executable and argv stay literal.
		property("ExecStartEx", []transientExecV1{{Path: executable, Argv: slices.Clone(argv), Flags: []string{"no-env-expand"}}}),
		property("Environment", supervisorLaunchEnv(unit)),
		property("UnsetEnvironment", []string{holderLockFDEnv}),
	}
	if m.Limits.MemoryMax > 0 {
		properties = append(properties, property("MemoryMax", uint64(m.Limits.MemoryMax)), property("MemorySwapMax", uint64(0)))
	}
	if err := manager.limitSessions(ctx, DefaultMemoryMax(), admission); err != nil {
		warnSession(m.ID, "could not limit the memory of all sessions", "err", err)
	}
	var job dbus.ObjectPath
	var call *dbus.Call
	err = admission.gate(ctx, func() error {
		if err := checkStartingMemoryLockedV1(ctx, admission.startup, m); err != nil {
			return err
		}
		manager.events.markDispatched()
		_ = manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer manager.connection.SetWriteDeadline(time.Time{})
		call = manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(manager.ctx,
			systemdManagerInterface+".StartTransientUnit", 0, make(chan *dbus.Call, 1), unit, "fail", properties, []transientAuxV1{})
		return nil
	})
	if err != nil {
		return nil, err
	}
	call = <-call.Done
	if err := call.Store(&job); err != nil {
		switch call.Err.(type) {
		case dbus.Error, *dbus.Error:
			manager.events.recordNegativeReply()
		}
		return nil, errors.Join(err, manager.checkNativeManager(), manager.events.failure())
	}
	if !job.IsValid() || job == "/" {
		return nil, sessionLeaseError("the native manager returned no exact original startup job")
	}
	path, err := manager.events.waitJob(ctx, job)
	if err != nil {
		return nil, err
	}
	observed, err := manager.activation(ctx, path, unit)
	if err != nil {
		return nil, err
	}
	if err := manager.events.bindActivation(observed); err != nil {
		return nil, err
	}
	pidfd, err := unix.PidfdOpen(int(observed.MainPID), 0)
	if err != nil {
		return nil, err
	}
	proc, err := os.OpenRoot(fmt.Sprintf("/proc/%d", observed.MainPID))
	if err != nil {
		return nil, errors.Join(err, unix.Close(pidfd))
	}
	activation := &nativeUnitActivationV1{manager: manager, path: path, unit: unit, properties: observed,
		peer:       &nativeStartupPeerV1{pid: int(observed.MainPID), uid: uint32(os.Getuid()), pidfd: pidfd, proc: proc},
		executable: info, argv: slices.Clone(argv), memoryMax: m.Limits.MemoryMax}
	if err := activation.Check(ctx); err != nil {
		return nil, errors.Join(err, activation.Close())
	}
	return activation, nil
}

// supervisorLaunchEnv closes the helper's host-root inputs over the original
// outside process. It does not inherit a manager's ambient alternate roots.
func supervisorLaunchEnv(unit string) []string {
	if unit == "" {
		env := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if name != holderLockFDEnv && name != unitEnv {
				env = append(env, entry)
			}
		}
		return append(env, unitEnv+"=")
	}
	names := []string{"HOME", "USER", "LOGNAME", "SHELL", "PATH", "LANG", "LC_ALL", "TZ",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"}
	env := make([]string, 0, len(names)+1)
	for _, name := range names {
		// Empty explicitly clears a manager's alternate root rather than
		// silently inheriting it. Other manager environment, such as the
		// host's alert-command credentials, retains its established scope.
		env = append(env, name+"="+os.Getenv(name))
	}
	return append(env, unitEnv+"="+unit)
}

func (activation *nativeUnitActivationV1) Check(ctx context.Context) error {
	if activation == nil || activation.peer == nil || activation.manager == nil {
		return sessionLeaseError("startup lost its actual native activation owner")
	}
	// The manager owns this first exec job. The live pidfd brackets the older
	// GetUnitByPID query, so systemd 252/kernel 6.1 need no newer-only method.
	if err := activation.peer.checkLive(); err != nil {
		return err
	}
	var path dbus.ObjectPath
	err := activation.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx,
		systemdManagerInterface+".GetUnitByPID", 0, activation.properties.MainPID).Store(&path)
	if err != nil || path != activation.path {
		return errors.Join(sessionLeaseError("the original live helper is no longer in its owned activation"), err)
	}
	current, err := activation.manager.activation(ctx, activation.path, activation.unit)
	if err != nil || !sameUnitActivationProperties(activation.properties, current) {
		return errors.Join(sessionLeaseError("the original owned invocation changed before startup handoff"), err)
	}
	if err := activation.manager.events.bindActivation(current); err != nil {
		return err
	}
	if activation.memoryMax > 0 {
		for _, item := range []struct {
			name string
			want uint64
		}{{"MemoryMax", uint64(activation.memoryMax)}, {"MemorySwapMax", 0}} {
			value, err := activation.manager.getProperty(ctx, activation.path, systemdServiceInterface, item.name)
			got, ok := value.Value().(uint64)
			if err != nil || !ok || got != item.want {
				return errors.Join(sessionLeaseError("the original native unit cap changed during startup"), err)
			}
		}
	}
	if err := checkStartupExecutable(activation.peer.proc, activation.executable, activation.argv); err != nil {
		return err
	}
	cgroup, err := activation.peer.proc.ReadFile("cgroup")
	paths, _ := cgroupPaths(cgroup)
	if err != nil || len(paths) == 0 {
		return errors.Join(sessionLeaseError("the original startup process lost its native unit cgroup"), err)
	}
	exact := false
	for _, parts := range paths {
		if strings.Join(parts, "/") == activation.properties.ControlGroup {
			exact = true
		}
	}
	if !exact || placeAmong(paths, activation.unit) != placeOwn {
		return sessionLeaseError("the native helper peer left its actual owned unit")
	}
	return errors.Join(activation.peer.checkLive(), activation.manager.checkNativeManager(), activation.manager.events.failure(), ctx.Err())
}

func (manager *nativeUserManagerV1) limitSessions(ctx context.Context, limit int64, admission *runtimePublication) error {
	if limit <= 0 || !memoryDelegated() {
		return nil
	}
	if have, ok := sliceBytes("memory.max"); ok && have == limit {
		return nil
	}
	if used, ok := sliceBytes("memory.current"); ok && used > limit {
		return fmt.Errorf("the sessions use %s together, more than the new limit of %s, so a later start sets it", mibText(used), mibText(limit))
	}
	var call *dbus.Call
	err := admission.gate(ctx, func() error {
		_ = manager.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer manager.connection.SetWriteDeadline(time.Time{})
		call = manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).GoWithContext(manager.ctx,
			systemdManagerInterface+".SetUnitProperties", 0, make(chan *dbus.Call, 1), brand.Slice, false,
			[]transientPropertyV1{{"MemoryMax", dbus.MakeVariant(uint64(limit))}})
		return nil
	})
	if err != nil {
		return err
	}
	call = <-call.Done
	return errors.Join(call.Err, manager.checkNativeManager())
}

func checkStartupExecutable(proc *os.Root, expected os.FileInfo, argv []string) error {
	actual, err := startupExecutableInfo(proc)
	if err != nil || expected == nil || !os.SameFile(expected, actual) {
		return errors.Join(sessionLeaseError("the native startup process has another executable"), err)
	}
	raw, err := proc.ReadFile("cmdline")
	actualArgs := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if err != nil || !slices.Equal(actualArgs, argv) {
		return errors.Join(sessionLeaseError("the actual startup executable did not receive its original literal arguments"), err)
	}
	return nil
}

func (activation *nativeUnitActivationV1) Close() error {
	if activation == nil {
		return nil
	}
	activation.once.Do(func() { activation.closeErr = activation.peer.Close() })
	return activation.closeErr
}

func startupExecutableInfo(proc *os.Root) (os.FileInfo, error) {
	directory, err := proc.Open(".")
	if err != nil {
		return nil, err
	}
	// /proc/<pinned-process>/exe is the one intentional kernel magic-link
	// read. Openat uses the retained proc directory, never a recovered PID.
	fd, err := unix.Openat(int(directory.Fd()), "exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	err = errors.Join(err, directory.Close())
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "native-startup-executable")
	info, err := file.Stat()
	return info, errors.Join(err, file.Close())
}
