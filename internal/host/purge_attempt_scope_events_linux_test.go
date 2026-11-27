//go:build linux

package host

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// These exercise strict observations only. Synthetic signals never create an
// actual scope, manager, native child, migration grant or reaping authority.
func TestPurgeScopeLateJobRequiresSameOriginalNewAndRemovedV3(t *testing.T) {
	events := &purgeAttemptScopeEventsV3{unit: "owned.scope", jobs: map[dbus.ObjectPath]*purgeAttemptScopeJobV3{}, wake: make(chan struct{}, 1)}
	job := dbus.ObjectPath("/org/freedesktop/systemd1/job/7")
	unit := dbus.ObjectPath("/org/freedesktop/systemd1/unit/owned_2escope")
	events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".JobRemoved", Body: []any{uint32(7), job, "owned.scope", "done"}})
	if err := events.checkOriginalJobV3(events.jobs[job]); err == nil {
		t.Fatal("terminal job DATA without its actual JobNew was accepted")
	}
	events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".UnitNew", Body: []any{"owned.scope", unit}})
	events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".JobNew", Body: []any{uint32(7), job, "owned.scope"}})
	if err := events.checkOriginalJobV3(events.jobs[job]); err != nil {
		t.Fatal("same original late observation failed", err)
	}
	events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".UnitRemoved", Body: []any{"owned.scope", unit}})
	events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".UnitNew", Body: []any{"owned.scope", unit}})
	if events.failureV3() == nil {
		t.Fatal("a removed/recreated scope renewed its original generation")
	}
}

func TestPurgeScopeRejectsAnotherJobAndForgedNativeCustodyV3(t *testing.T) {
	events := &purgeAttemptScopeEventsV3{unit: "owned.scope", jobs: map[dbus.ObjectPath]*purgeAttemptScopeJobV3{}, wake: make(chan struct{}, 1)}
	first := dbus.ObjectPath("/org/freedesktop/systemd1/job/1")
	second := dbus.ObjectPath("/org/freedesktop/systemd1/job/2")
	for index, path := range []dbus.ObjectPath{first, second} {
		events.observeV3(&dbus.Signal{Path: systemdManagerPath, Name: systemdManagerInterface + ".JobNew", Body: []any{uint32(index + 1), path, "owned.scope"}})
	}
	if err := events.checkOnlyOriginalJobsV3(events.jobs[first], nil); err == nil {
		t.Fatal("a foreign replacement job was accepted as original scope custody")
	}
	owner := &purgeAttemptExecV1{}
	for _, scope := range []*purgeAttemptScopeV3{{}, {owner: owner, adopted: true, notified: true}, {owner: owner, dispatched: true, negative: true}} {
		if err := scope.checkTerminalForReapV3(); err == nil {
			t.Fatal("job/phase-shaped DATA released native reaping")
		}
		if err := scope.checkLiveV3(t.Context()); err == nil {
			t.Fatal("job/phase-shaped DATA established scope membership")
		}
		if err := scope.closeAfterCreationV3(); err == nil {
			t.Fatal("metadata established original scope cleanup")
		}
	}
}
