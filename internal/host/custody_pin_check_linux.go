//go:build linux

package host

import (
	"context"
	"errors"
	"slices"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/brand"
)

func custodyTargetV1() string { return brand.Name + "-custody.target" }

// checkCustodyPinV1 validates a passive ordering reference on the actual held
// manager. Configuration ownership and native FD transfer remain independent.
func (manager *nativeUserManagerV1) checkCustodyPinV1(ctx context.Context, unit string) error {
	if err := manager.checkNativeManager(); err != nil {
		return err
	}
	var target dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnit", 0, custodyTargetV1()).Store(&target); err != nil {
		return err
	}
	for _, property := range []struct{ name, want string }{{"Id", custodyTargetV1()}, {"LoadState", "loaded"}, {"ActiveState", "active"}} {
		value, err := manager.getProperty(ctx, target, systemdUnitInterface, property.name)
		if err != nil {
			return err
		}
		if actual, ok := value.Value().(string); !ok || actual != property.want {
			return sessionLeaseError("the original processless custody anchor is not active and loaded")
		}
	}
	for _, property := range []string{"Wants", "Requires", "BindsTo", "Upholds"} {
		value, err := manager.getProperty(ctx, target, systemdUnitInterface, property)
		if err != nil {
			return err
		}
		units, ok := value.Value().([]string)
		if !ok || slices.Contains(units, unit) {
			return sessionLeaseError("the custody anchor has a session pull-in dependency")
		}
	}
	value, err := manager.getProperty(ctx, target, systemdUnitInterface, "After")
	if err != nil {
		return err
	}
	units, ok := value.Value().([]string)
	if !ok || !slices.Contains(units, unit) {
		return sessionLeaseError("the original custody service lost its passive GC reference")
	}
	return errors.Join(ctx.Err(), manager.checkNativeManager())
}
