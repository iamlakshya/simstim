//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Ordinary systemd-run uses Type=simple. Its authenticated live invocation
// is captured separately from the walled Type=exec creation protocol.
type ordinaryInvocationV1 struct {
	manager       *nativeUserManagerV1
	peer          *nativeStartupPeerV1
	path          dbus.ObjectPath
	unit, primary string
	activation    ordinaryActivationV1
	cgroup        *os.File
	maximum       int64
}
type ordinaryActivationV1 struct {
	id, controlGroup, kind string
	invocation             [16]byte
	pid                    uint32
}

func captureOrdinaryInvocationV1(ctx context.Context, owner *ordinaryHolderCaptureV2) (_ *ordinaryInvocationV1, returned error) {
	data, err := owner.peer.proc.ReadFile("cgroup")
	if err != nil {
		return nil, err
	}
	paths, _ := cgroupPaths(data)
	if len(paths) == 0 {
		return nil, sessionLeaseError("the actual ordinary holder has unknown native placement")
	}
	unit := UnitName(owner.operation.Primary())
	expected := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", os.Getuid(), os.Getuid(), brand.Slice, unit)
	if !bytes.Equal(data, []byte(expected)) {
		if owner.source.Run.Unit != "" || placeAmong(paths, "") != placeOutside {
			return nil, sessionLeaseError("the ordinary holder is outside its declared canonical native unit")
		}
		// This genuine current unconfined holder owns only its bounded
		// objects. A manager appearing later cannot enlarge that authority.
		return nil, owner.peer.checkLive()
	}
	manager, err := openNativeUserManagerV1(ctx)
	if err != nil {
		return nil, err
	}
	result := &ordinaryInvocationV1{manager: manager, peer: owner.peer, primary: owner.operation.Primary(), unit: unit, maximum: owner.source.Limits.MemoryMax}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, result.Close())
		}
	}()
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(owner.peer.pid)).Store(&result.path); err != nil {
		return nil, err
	}
	result.activation, err = result.readActivation(ctx, result.path)
	if err != nil {
		return nil, err
	}
	result.cgroup, err = openNativeStopCgroupV1(result.primary)
	if err != nil {
		return nil, err
	}
	if err := result.Check(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}

func (invocation *ordinaryInvocationV1) readActivation(ctx context.Context, path dbus.ObjectPath) (ordinaryActivationV1, error) {
	var result ordinaryActivationV1
	values := map[string]dbus.Variant{}
	for _, role := range []struct{ iface, name string }{
		{systemdUnitInterface, "Id"}, {systemdUnitInterface, "ActiveState"}, {systemdUnitInterface, "InvocationID"},
		{systemdServiceInterface, "MainPID"}, {systemdServiceInterface, "ControlGroup"}, {systemdServiceInterface, "Type"},
		{systemdServiceInterface, "NRestarts"}, {systemdServiceInterface, "KillMode"}, {systemdServiceInterface, "Restart"},
	} {
		value, err := invocation.manager.getProperty(ctx, path, role.iface, role.name)
		if err != nil {
			return result, err
		}
		values[role.name] = value
	}
	id, idOK := values["Id"].Value().(string)
	state, stateOK := values["ActiveState"].Value().(string)
	token, tokenOK := values["InvocationID"].Value().([]byte)
	pid, pidOK := values["MainPID"].Value().(uint32)
	group, groupOK := values["ControlGroup"].Value().(string)
	kind, kindOK := values["Type"].Value().(string)
	restarts, restartsOK := values["NRestarts"].Value().(uint32)
	killMode, killOK := values["KillMode"].Value().(string)
	restart, restartOK := values["Restart"].Value().(string)
	expected := fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service/%s/%s", os.Getuid(), os.Getuid(), brand.Slice, invocation.unit)
	if !idOK || id != invocation.unit || !stateOK || state != "active" || !tokenOK || len(token) != 16 || !pidOK || pid != uint32(invocation.peer.pid) || !groupOK || group != expected || !kindOK || kind != "simple" || !restartsOK || restarts != 0 || !killOK || killMode != "mixed" || !restartOK || restart != "no" {
		return result, sessionLeaseError("the ordinary manager returned no exact first simple holder invocation")
	}
	copy(result.invocation[:], token)
	if result.invocation == [16]byte{} {
		return result, sessionLeaseError("the original ordinary invocation ID is empty")
	}
	result.id, result.controlGroup, result.kind, result.pid = id, group, kind, pid
	if invocation.maximum > 0 {
		for _, cap := range []struct {
			name string
			want uint64
		}{{"MemoryMax", uint64(invocation.maximum)}, {"MemorySwapMax", 0}} {
			value, err := invocation.manager.getProperty(ctx, path, systemdServiceInterface, cap.name)
			actual, ok := value.Value().(uint64)
			if err != nil || !ok || actual != cap.want {
				return result, errors.Join(sessionLeaseError("the ordinary holder lost its declared native memory cap"), err)
			}
		}
	}
	return result, nil
}

func (invocation *ordinaryInvocationV1) Check(ctx context.Context) error {
	if invocation == nil || invocation.manager == nil || invocation.peer == nil || invocation.cgroup == nil {
		return sessionLeaseError("the original ordinary invocation ended")
	}
	if err := errors.Join(ctx.Err(), invocation.peer.checkLive(), invocation.manager.checkNativeManager()); err != nil {
		return err
	}
	var path dbus.ObjectPath
	if err := invocation.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(invocation.peer.pid)).Store(&path); err != nil {
		return err
	}
	actual, err := invocation.readActivation(ctx, path)
	if err != nil || path != invocation.path || actual != invocation.activation {
		return errors.Join(sessionLeaseError("the original ordinary native invocation changed"), err)
	}
	data, err := invocation.peer.proc.ReadFile("cgroup")
	if err != nil || !bytes.Equal(data, []byte("0::"+invocation.activation.controlGroup+"\n")) {
		return errors.Join(sessionLeaseError("the original ordinary holder left its captured native group"), err)
	}
	fresh, err := openNativeStopCgroupV1(invocation.primary)
	if err != nil {
		return err
	}
	original, originalErr := invocation.cgroup.Stat()
	current, currentErr := fresh.Stat()
	closed := fresh.Close()
	if originalErr != nil || currentErr != nil || !os.SameFile(original, current) {
		return errors.Join(sessionLeaseError("the actual ordinary cgroup object was replaced"), originalErr, currentErr, closed)
	}
	return errors.Join(closed, invocation.peer.checkLive(), invocation.manager.checkNativeManager(), ctx.Err())
}

func (invocation *ordinaryInvocationV1) stopped(ctx context.Context) error {
	if err := errors.Join(invocation.manager.checkNativeManager(), ctx.Err()); err != nil {
		return err
	}
	empty, err := originalStopCgroupEmptyV1(invocation.cgroup)
	if err != nil || !empty {
		return errors.Join(sessionLeaseError("the original ordinary unit has uncertain or remaining processes"), err)
	}
	if err := invocation.manager.requireAuxiliaryUnitAbsentV1(ctx, invocation.unit); err != nil {
		return err
	}
	group, err := readMemoryUnitV1(ctx, invocation.primary, true)
	if err != nil || group.exists {
		return errors.Join(sessionLeaseError("the canonical ordinary unit cgroup still exists"), err)
	}
	return errors.Join(invocation.manager.requireAuxiliaryUnitAbsentV1(ctx, invocation.unit), invocation.manager.checkNativeManager(), ctx.Err())
}
func (invocation *ordinaryInvocationV1) settle() error {
	// The authenticated holder already joined its bounded objects and exited.
	// Kill only its retained original cgroup if remaining unit children persist.
	grace := time.Now().Add(30 * time.Second)
	killed := false
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := invocation.stopped(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if invocation.manager.checkNativeManager() != nil {
			// Unknown original manager closure retains the stack and SAME OP.
			select {}
		}
		if !killed && !time.Now().Before(grace) {
			if err := killOriginalStopCgroupV1(invocation.cgroup); err != nil {
				select {}
			}
			killed = true
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (invocation *ordinaryInvocationV1) Close() error {
	if invocation == nil {
		return nil
	}
	var failure error
	if invocation.cgroup != nil {
		failure = errors.Join(failure, invocation.cgroup.Close())
	}
	if invocation.manager != nil {
		failure = errors.Join(failure, invocation.manager.Close())
	}
	return failure
}
