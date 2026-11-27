//go:build linux

package guest

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestVMShutdownSyncsAndUnmountsBeforePowerOff(t *testing.T) {
	var order []string
	fs := newVMFiles("/", vmMountOps{pivot: func(a, b string) error { order = append(order, "pivot"); return nil }, chdir: func(string) error { return nil }, unmount: func(name string, _ int) error { order = append(order, "unmount:"+name); return nil }})
	fs.pivoted = true
	fs.mounts = []vmMounted{{target: "/mnt/root"}, {target: "/mnt/root/.tools"}, {target: "/mnt/root/work"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vm := &vmRuntime{init: &Init{}, files: fs, ctx: ctx, cancel: cancel}
	if err := vm.shutdownWith(vmShutdownOps{sync: func() { order = append(order, "sync") }, powerOff: func() error { order = append(order, "poweroff"); return nil }}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"sync", "pivot", "unmount:/mnt/root/work", "unmount:/mnt/root", "poweroff"}) {
		t.Fatal("poweroff bypassed ordered filesystem cleanup", order)
	}
	if ctx.Err() == nil {
		t.Fatal("guest network/command lifetime not cancelled")
	}
}

func TestVMShutdownFailureNeverReportsCleanPowerOff(t *testing.T) {
	for _, failure := range []string{"pivot", "unmount", "prior"} {
		t.Run(failure, func(t *testing.T) {
			powered := false
			fs := newVMFiles("/", vmMountOps{pivot: func(string, string) error {
				if failure == "pivot" {
					return errors.New("pivot refused")
				}
				return nil
			}, chdir: func(string) error { return nil }, unmount: func(string, int) error {
				if failure == "unmount" {
					return errors.New("busy disk")
				}
				return nil
			}})
			fs.pivoted = true
			fs.mounts = []vmMounted{{target: "/mnt/root"}}
			_, cancel := context.WithCancel(context.Background())
			defer cancel()
			vm := &vmRuntime{init: &Init{}, files: fs, cancel: cancel}
			if failure == "prior" {
				vm.endErr = errors.New("PTY cleanup failed")
			}
			if err := vm.shutdownWith(vmShutdownOps{sync: func() {}, powerOff: func() error { powered = true; return nil }}); err == nil || powered {
				t.Fatal("failed cleanup became a clean owned VMM exit", err, powered)
			}
		})
	}
}

func TestVMPostPivotCgroupMountIsTrackedBeforeParentSysfsUnmount(t *testing.T) {
	var targets []string
	fs := newVMFiles("/", vmMountOps{mount: func(string, string, string, uintptr, string) error { return nil }, unmount: func(name string, _ int) error { targets = append(targets, name); return nil }})
	fs.mounts = []vmMounted{{target: "/mnt/root/sys"}}
	fs.pivoted = true
	if err := fs.mountLive("cgroup2", "/sys/fs/cgroup", "cgroup2", 0, ""); err != nil {
		t.Fatal(err)
	}
	fs.back = true
	if err := fs.close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(targets, []string{"/mnt/root/sys/fs/cgroup", "/mnt/root/sys"}) {
		t.Fatal("untracked child mount prevents clean shutdown", targets)
	}
}

func TestVMStateReadyCannotPrecedeUnmountOrSurviveCleanupFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var order []string
		fs := newVMFiles("/", vmMountOps{unmount: func(string, int) error {
			order = append(order, "unmount")
			if fail {
				return errors.New("busy")
			}
			return nil
		}})
		fs.mounts = []vmMounted{{target: "/mnt/session"}}
		_, cancel := context.WithCancel(context.Background())
		defer cancel()
		vm := &vmRuntime{init: &Init{}, files: fs, cancel: cancel}
		err := vm.shutdownWith(vmShutdownOps{sync: func() { order = append(order, "sync") }, ready: func() error { order = append(order, "state_ready"); return nil }, powerOff: func() error { order = append(order, "poweroff"); return nil }})
		if fail {
			if err == nil || !reflect.DeepEqual(order, []string{"sync", "unmount"}) {
				t.Fatal("failed cleanup still published pending state", order, err)
			}
		} else if err != nil || !reflect.DeepEqual(order, []string{"sync", "unmount", "state_ready", "poweroff"}) {
			t.Fatal("state readiness preceded actual filesystem cleanup", order, err)
		}
	}
}
