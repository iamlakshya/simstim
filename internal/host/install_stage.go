package host

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This live capability comes only from the actual mapped staged executable.
// It is never encoded, loaded from a path list or reconstructed on retry.
type installStageV1 struct {
	root                  *os.Root
	file                  *os.File
	directory, executable purgeRef
	nonce                 string
}

func (stage *installStageV1) close() (returned error) {
	defer func() { returned = errors.Join(returned, stage.file.Close(), stage.root.Close()) }()
	info, err := cachefs.ValidateFile(stage.root, brand.HostBinary, stage.file, 0o755)
	if err != nil || !stage.executable.matches(info) {
		return placementError("preserved a changed staged helper instead of deleting another inode")
	}
	capture := ".install-self-" + stage.nonce
	if err := cachefs.RenameNoReplace(stage.root, brand.HostBinary, capture); err != nil {
		return err
	}
	info, err = cachefs.ValidateFile(stage.root, capture, stage.file, 0o755)
	if err != nil || !stage.executable.matches(info) {
		return placementError("preserved an unexpected staged helper capture")
	}
	if err := stage.root.Remove(capture); err != nil {
		return err
	}
	if err := syncCapturedDirectory(stage.root); err != nil {
		return err
	}
	parent, err := cachefs.OpenSafeAncestor(filepath.Dir(stage.root.Name()))
	if err != nil {
		return err
	}
	defer parent.Close()
	leaf := filepath.Base(stage.root.Name())
	current, err := parent.Lstat(leaf)
	if err != nil || !stage.directory.matches(current) {
		return placementError("preserved a replaced installation staging directory")
	}
	destination := "." + brand.Name + "-install-cleanup-" + stage.nonce
	if err := cachefs.RenameOwnedDirectoryNoReplace(parent, leaf, destination); err != nil {
		return err
	}
	current, err = parent.Lstat(destination)
	if err != nil || !stage.directory.matches(current) {
		return placementError("preserved another captured staging directory")
	}
	// Empty-only removal retains every unexpected child, even after a failed
	// install. It never recursively removes a tool-like name in the home.
	if err := parent.Remove(destination); err != nil {
		return err
	}
	return syncCapturedDirectory(parent)
}
