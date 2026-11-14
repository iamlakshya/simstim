package image

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type createdImageFile struct {
	Name       string
	Parent     cachefs.Reference
	File       cachefs.Reference
	Descriptor *os.File
}

func (s *Store) captureCreated(name string, file *os.File, mode os.FileMode) (createdImageFile, error) {
	root, leaf, err := s.parent(name)
	if err != nil {
		return createdImageFile{}, err
	}
	defer root.Close()
	parent, err := cachefs.CaptureRoot(root)
	if err != nil {
		return createdImageFile{}, err
	}
	reference, err := cachefs.CaptureFile(root, leaf, file, mode)
	if err != nil {
		return createdImageFile{}, err
	}
	return createdImageFile{Name: name, Parent: parent, File: reference, Descriptor: file}, nil
}

// cleanupCreated is only for this creator's mutable uncommitted part. A new
// descriptor at the old name never becomes its replacement cleanup authority.
func (s *Store) cleanupCreated(part createdImageFile, mode os.FileMode) error {
	root, leaf, err := s.parent(part.Name)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := part.Parent.CheckRoot(root); err != nil {
		return err
	}
	if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := part.File.CheckCreatedFile(root, leaf, part.Descriptor, mode); err != nil {
		return ownershipError("uncommitted image part changed native identity: " + err.Error())
	}
	// Once creator writes have stopped, freeze the current complete witness for
	// the quarantine capture and its post-capture comparison.
	current, err := witnessFile(root, leaf, part.Descriptor, mode, false)
	if err != nil {
		return err
	}
	quarantine, err := randomLeaf(".part-clean-")
	if err != nil {
		return err
	}
	if err := captureLeaf(root, leaf, quarantine, current, mode); err != nil {
		return err
	}
	if err := root.Remove(quarantine); err != nil {
		return err
	}
	return syncRoot(root)
}
