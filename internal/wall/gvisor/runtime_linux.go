//go:build linux

package gvisor

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"os"
	"path"
)

func (d *Driver) openRuntime(ctx context.Context) (owned *Runtime, returned error) {
	manifest, err := d.options.ExpectedRuntime(ctx)
	if err != nil {
		return nil, err
	}
	items, err := runtimeItems(manifest)
	if err != nil {
		return nil, err
	}
	domain, err := d.options.Domain(ctx)
	if err != nil {
		return nil, err
	}
	owned = &Runtime{}
	store, err := runtimecache.OpenHostReadOnly(ctx, d.options.Artefacts, domain)
	owned.store = store
	if err != nil {
		return owned, runtimeFileError("store", err)
	}

	for _, item := range items {
		file, err := store.OpenItem(ctx, item)
		if err != nil {
			if file != nil {
				owned.pendingFiles = append(owned.pendingFiles, file)
			}
			return owned, runtimeFileError(item.Name, err)
		}
		sealed, sealErr := supply.SealRuntime(ctx, item, file)
		closeErr := file.Close()
		if closeErr != nil {
			owned.pendingFiles = append(owned.pendingFiles, file)
			owned.closeErr = errors.Join(owned.closeErr, closeErr)
		}
		if err := errors.Join(sealErr, closeErr); err != nil {
			if sealed != nil {
				owned.pendingFiles = append(owned.pendingFiles, sealed)
			}
			return owned, runtimeFileError(item.Name, err)
		}
		file = sealed
		target := path.Join(runtimeRoot(), "gvisor-bin", item.Name)
		if item.Name == "runsc" {
			owned.program, owned.item = file, item
			target = path.Join(runtimeRoot(), "runsc")
		} else {
			owned.files = append(owned.files, file)
		}
		owned.executables = append(owned.executables, Executable{Target: target, File: file})
	}
	owned.protectedSuite, err = captureProtectedSuiteV1(ctx, owned, items)
	if err != nil {
		return owned, err
	}
	if err := owned.store.Close(); err != nil {
		return owned, err
	}
	owned.store = nil
	return owned, nil
}

func runtimeFileError(name string, err error) error {
	var mismatch *supply.MismatchError
	if errors.Is(err, os.ErrNotExist) || errors.As(err, &mismatch) {
		return failure("runtime_missing", "the host lacks its client's verified gvisor runtime file "+name, err)
	}
	return err
}
