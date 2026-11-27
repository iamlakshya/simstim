package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/checkpoint"
)

// Only an actual stopped-source constructor fills this owner. Local roots
// are private OP-bound descriptors; slots retain H15's separate native owner.
type rewindStoppedStorageV1 struct {
	preparation  *bridgeRewindPreparationV1
	root, vm     *os.Root
	originalDisk *os.File
	store        *checkpoint.Store
	closeNative  func() error
	closed       bool
	openErr      error
	closeErr     error
}

func (storage *rewindStoppedStorageV1) closeV1() error {
	if storage == nil {
		return nil
	}
	if storage.closeErr != nil || storage.closed {
		return storage.closeErr
	}
	if storage.store != nil {
		if err := storage.store.Close(); err != nil {
			return err
		}
	}
	if storage.closeNative != nil {
		if err := storage.closeNative(); err != nil {
			return err
		}
		storage.closeNative = nil
	}
	if storage.originalDisk != nil {
		storage.closeErr = errors.Join(storage.closeErr, storage.originalDisk.Close())
		if storage.closeErr == nil {
			storage.originalDisk = nil
		}
	}
	if storage.vm != nil {
		err := storage.vm.Close()
		storage.closeErr = errors.Join(storage.closeErr, err)
		if err == nil {
			storage.vm = nil
		}
	}
	if storage.root != nil {
		err := storage.root.Close()
		storage.closeErr = errors.Join(storage.closeErr, err)
		if err == nil {
			storage.root = nil
		}
	}
	if storage.closeErr == nil {
		storage.closed = true
	}
	return storage.closeErr
}
