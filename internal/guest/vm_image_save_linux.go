//go:build linux

package guest

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/image"
	"golang.org/x/sys/unix"
)

// captureImageSaveRootsV1 runs immediately after this original overlay mount,
// before bind projection and pivot. In reset mode the actual upper's tmpfs is
// detached later; only this captured native handle can retain its real source.
func (files *vmFiles) captureImageSaveRootsV1(upperBase string) error {
	if files.copyNativeOwner == nil {
		return nil
	} // Fixture/metadata mounts mint no roots.
	if err := files.copyNativeOwner.check(files.copyNativeOwner.init); err != nil {
		return err
	}
	if files.imageSaveRoot != nil || files.imageSaveUpper != nil {
		return vmError("the original saved-root handles were already captured")
	}
	root, err := os.OpenRoot(files.root)
	if err != nil {
		return err
	}
	upper, err := os.OpenRoot(filepath.Join(upperBase, "upper"))
	if err != nil {
		return errors.Join(err, root.Close())
	}
	if err := files.copyNativeOwner.check(files.copyNativeOwner.init); err != nil {
		return errors.Join(err, upper.Close(), root.Close())
	}
	files.imageSaveRoot, files.imageSaveUpper = root, upper
	return nil
}
func (files *vmFiles) closeImageSaveRootsV1() error {
	var result error
	if files.imageSaveRoot != nil {
		result = errors.Join(result, files.imageSaveRoot.Close())
		files.imageSaveRoot = nil
	}
	if files.imageSaveUpper != nil {
		result = errors.Join(result, files.imageSaveUpper.Close())
		files.imageSaveUpper = nil
	}
	return result
}

// imageSaveRootsV1 is available only on this actual verified VM constructor.
// The caller already holds its control-operation receipt, so cleanup cannot
// close these originals until the SAME reservation has cancelled and joined.
func (vm *vmRuntime) imageSaveRootsV1() (*os.Root, *os.Root, image.SavedRootPlan, error) {
	if err := vm.connection.check(vm.init); err != nil {
		return nil, nil, image.SavedRootPlan{}, err
	}
	vm.mu.Lock()
	prepared := vm.applied && !vm.failed
	state := vm.plan.State
	vm.mu.Unlock()
	if !prepared || vm.files.imageSaveRoot == nil || vm.files.imageSaveUpper == nil {
		return nil, nil, image.SavedRootPlan{}, vmError("image save has no original prepared overlay roots")
	}
	root, err := vm.files.imageSaveRoot.OpenRoot(".")
	if err != nil {
		return nil, nil, image.SavedRootPlan{}, err
	}
	upper, err := vm.files.imageSaveUpper.OpenRoot(".")
	if err != nil {
		return nil, nil, image.SavedRootPlan{}, errors.Join(err, root.Close())
	}
	plan, err := image.NewSavedRootPlan(state, state, "", nil, nil)
	if err != nil {
		return nil, nil, image.SavedRootPlan{}, errors.Join(err, upper.Close(), root.Close())
	}
	if err := vm.connection.check(vm.init); err != nil {
		return nil, nil, image.SavedRootPlan{}, errors.Join(err, upper.Close(), root.Close())
	}
	return root, upper, plan, nil
}

// Sync only this actual verified VM's original captured merged filesystem.
// No generic host/test-root path or supplied descriptor can request a flush.
func (vm *vmRuntime) syncImageSaveRootV1(root *os.Root) error {
	if err := vm.connection.check(vm.init); err != nil {
		return err
	}
	if root == nil || vm.files == nil || vm.files.imageSaveRoot == nil {
		return vmError("image sync has no actual original merged root")
	}
	wanted, err := vm.files.imageSaveRoot.Stat(".")
	if err != nil {
		return err
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(wanted, actual) {
		return errors.Join(vmError("image sync root differs from its native constructor"), err, file.Close())
	}
	syncErr := unix.Syncfs(int(file.Fd()))
	return errors.Join(syncErr, file.Close(), vm.connection.check(vm.init))
}
