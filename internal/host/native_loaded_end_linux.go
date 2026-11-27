//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// This cleanup-only observation belongs to the exact original StartUnit job.
// It supplies neither a process handle nor permission to signal a service.
// Before its startup loan the helper cannot have created a backend runtime.
func (manager *nativeUserManagerV1) checkLoadedOriginalEndV1(ctx context.Context) error {
	events := &manager.events
	events.mu.Lock()
	unit, path, invocation, previous := events.unit, events.path, events.invocation, events.previousInvocation
	correlated := events.loaded && events.dispatched && !events.negativeReply && !events.unitRemoved && events.err == nil && events.job != "" && events.observedJob == events.job && events.removedJob == events.job && events.jobID != 0 && events.jobID == events.removedID && events.result != ""
	events.mu.Unlock()
	if !correlated {
		return sessionLeaseError("the loaded startup has no completed exact original job for cleanup")
	}
	if err := manager.checkNativeManager(); err != nil {
		return err
	}
	var currentPath dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, unit).Store(&currentPath); err != nil || currentPath != path {
		return errors.Join(sessionLeaseError("the original loaded startup unit changed before cleanup"), err)
	}
	values := make(map[string]dbus.Variant)
	for _, item := range []struct{ iface, name string }{{systemdUnitInterface, "Id"}, {systemdUnitInterface, "LoadState"}, {systemdUnitInterface, "ActiveState"}, {systemdUnitInterface, "InvocationID"}, {systemdServiceInterface, "MainPID"}, {systemdServiceInterface, "ControlPID"}, {systemdServiceInterface, "ControlGroup"}, {systemdServiceInterface, "NRestarts"}} {
		value, err := manager.getProperty(ctx, path, item.iface, item.name)
		if err != nil {
			return err
		}
		values[item.name] = value
	}
	id, idOK := values["Id"].Value().(string)
	load, loadOK := values["LoadState"].Value().(string)
	state, stateOK := values["ActiveState"].Value().(string)
	pid, pidOK := values["MainPID"].Value().(uint32)
	control, controlOK := values["ControlPID"].Value().(uint32)
	restarts, restartsOK := values["NRestarts"].Value().(uint32)
	raw, rawOK := values["InvocationID"].Value().([]byte)
	group, groupOK := values["ControlGroup"].Value().(string)
	if !idOK || id != unit || !loadOK || load != "loaded" || !stateOK || state != "inactive" && state != "failed" || !pidOK || pid != 0 || !controlOK || control != 0 || !restartsOK || restarts != 0 || !rawOK || (len(raw) != 0 && len(raw) != 16) || !groupOK || group != "" && group != fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service/%s/%s", os.Getuid(), os.Getuid(), brand.Slice, unit) {
		return sessionLeaseError("the original loaded job still has live or unknown native owners")
	}
	got := hex.EncodeToString(raw)
	if invocation != "" && got != invocation || invocation == "" && got != "" && got != previous && got != strings.Repeat("0", 32) {
		return sessionLeaseError("a different loaded invocation cannot settle the original startup")
	}
	// The exact job may end before a pidfd can be opened. At this stage no
	// loan was sent. Check the canonical cgroup through its real filesystem,
	// and never use this newly observed directory as a signalling handle.
	primary, keyErr := canonicalMemoryUnitKeyV1(unit)
	if keyErr != nil || UnitName(primary) != unit {
		return sessionLeaseError("the loaded cleanup lost its canonical primary")
	}
	groupFile, err := openNativeStopCgroupV1(primary)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if groupFile != nil {
		empty, inspectErr := originalStopCgroupEmptyV1(groupFile)
		closeErr := groupFile.Close()
		if inspectErr != nil || closeErr != nil || !empty {
			return errors.Join(sessionLeaseError("the original loaded startup still has native cgroup writers"), inspectErr, closeErr)
		}
	}
	events.mu.Lock()
	unchanged := events.err == nil && !events.unitRemoved && events.invocation == invocation && events.path == path && events.unit == unit
	events.mu.Unlock()
	if !unchanged {
		return sessionLeaseError("the original loaded cleanup changed during native observation")
	}
	return errors.Join(ctx.Err(), manager.checkNativeManager())
}

func (manager *nativeUserManagerV1) waitLoadedOriginalEndV1() error {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := manager.checkLoadedOriginalEndV1(ctx)
		cancel()
		if err == nil {
			return nil
		}
		// Unknown/replaced service or manager retains the actual original
		// controller, OP and charge. No timeout certifies physical closure.
		time.Sleep(20 * time.Millisecond)
	}
}

// A sent loan can admit real factories. It is excluded even if its ACK was
// lost, and even if the unit's current MainPID and cgroup are now absent.
func (launch *startupLaunchV1) canReconcileLoadedPreloanV1() bool {
	if launch == nil || !launch.started || launch.loanSent || launch.ready || launch.manager == nil {
		return false
	}
	events := &launch.manager.events
	events.mu.Lock()
	defer events.mu.Unlock()
	return events.loaded && events.dispatched && !events.negativeReply
}
