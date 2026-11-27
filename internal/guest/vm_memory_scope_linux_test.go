//go:build linux

package guest

import (
	"errors"
	"os"
	"testing"
)

func TestNativeVMMemoryFactsCannotOpenOutsideActualFixedInit(t *testing.T) {
	if os.Getpid() == 1 && os.Geteuid() == 0 {
		t.Fatal("this negative local fixture cannot run as an actual VM PID1")
	}
	if scope, err := openVMMemoryScope(t.Context()); err == nil || scope != nil {
		t.Fatal("a regular process obtained fixed-init native memory facts")
	}
}

func TestVMShutdownClosesMemoryFactsBeforeFilesystemTeardown(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	// This is only an owned descriptor for the cleanup-order fixture. No
	// Current, headroom or original boot/Need authority is fabricated.
	scope := &vmMemoryScope{root: root}
	vm := &vmRuntime{init: &Init{}, cancel: func() {}, files: newVMFiles(t.TempDir(), vmMountOps{}), memory: &vmMemory{scope: scope}}
	checked, powered := false, false
	err = vm.shutdownWith(vmShutdownOps{sync: func() {
		checked = true
		if _, err := root.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("filesystem teardown passed an open memory scope", err)
		}
	}, powerOff: func() error { powered = true; return nil }})
	if err != nil || !checked || !powered || !scope.closed {
		t.Fatal("owned memory facts did not close before teardown", err)
	}
	if err := scope.close(); err != nil {
		t.Fatal("second scope close repeated descriptor cleanup", err)
	}
}

func TestVMMemoryFactsCloseErrorPreventsCleanShutdown(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	scope := &vmMemoryScope{root: root}
	vm := &vmRuntime{init: &Init{}, cancel: func() {}, files: newVMFiles(t.TempDir(), vmMountOps{}), memory: &vmMemory{scope: scope}}
	err = vm.shutdownWith(vmShutdownOps{sync: func() { t.Fatal("cleanup failure reached filesystem teardown") }, powerOff: func() error { t.Fatal("cleanup failure powered off cleanly"); return nil }})
	if !errors.Is(err, os.ErrClosed) || !errors.Is(vm.endErr, os.ErrClosed) || !errors.Is(scope.close(), os.ErrClosed) {
		t.Fatal("memory scope cleanup failure or its retained result was lost", err)
	}
}
