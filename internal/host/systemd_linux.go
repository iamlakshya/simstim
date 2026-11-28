//go:build linux

package host

import (
	"context"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
)

// systemdBus is the subset of *dbus.Conn used to stop a session's unit, wait
// for systemd's job result, and clear a failed state so --collect unloads it.
// Tests replace dialSystemdBus with a fake.
type systemdBus interface {
	StopUnitContext(ctx context.Context, name string, mode string, ch chan<- string) (int, error)
	ResetFailedUnitContext(ctx context.Context, name string) error
	GetUnitPropertyContext(ctx context.Context, unit string, propertyName string) (*dbus.Property, error)
	Close()
}

var dialSystemdBus = func(ctx context.Context) (systemdBus, error) {
	return dbus.NewUserConnectionContext(ctx)
}

// stopUnitViaDBus stops unit through the user's systemd D-Bus manager, waits
// for the stop job's result on the completion channel, resets a failed state
// if the unit entered one, and waits briefly until --collect unloads the unit.
// It returns true when D-Bus handled the stop (including when the unit was
// already unloaded), or false when the caller should fall back to systemctl.
func stopUnitViaDBus(ctx context.Context, unit string) bool {
	conn, err := dialSystemdBus(ctx)
	if err != nil {
		return false
	}
	defer conn.Close()

	ch := make(chan string, 1)
	if _, err := conn.StopUnitContext(ctx, unit, "replace", ch); err != nil {
		return isNoSuchUnit(err)
	}
	select {
	case <-ch:
	case <-ctx.Done():
		return true
	}

	if active, ok := unitStringProp(ctx, conn, unit, "ActiveState"); ok && active == "failed" {
		_ = conn.ResetFailedUnitContext(ctx, unit)
	}

	deadline := time.Now().Add(3 * time.Second)
	resetTried := false
	for time.Now().Before(deadline) {
		load, ok := unitStringProp(ctx, conn, unit, "LoadState")
		if !ok || load != "loaded" {
			return true
		}
		if !resetTried {
			resetTried = true
			if active, ok := unitStringProp(ctx, conn, unit, "ActiveState"); ok && active == "failed" {
				_ = conn.ResetFailedUnitContext(ctx, unit)
			}
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(50 * time.Millisecond):
		}
	}
	return true
}

func unitStringProp(ctx context.Context, conn systemdBus, unit, name string) (string, bool) {
	prop, err := conn.GetUnitPropertyContext(ctx, unit, name)
	if err != nil || prop == nil {
		return "", false
	}
	s, ok := prop.Value.Value().(string)
	return s, ok
}

func isNoSuchUnit(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NoSuchUnit")
}
