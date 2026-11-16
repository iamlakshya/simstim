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
	store, err := runtimecache.OpenHostReadOnly(ctx, d.options.Artefacts, domain)
	if err != nil {
		return nil, runtimeFileError("store", err)
	}
	owned = &Runtime{}
	result := owned
	defer func() {
		returned = errors.Join(returned, store.Close())
		if returned != nil {
			returned = errors.Join(returned, result.Close())
			owned = nil
		}
	}()
	for _, item := range items {
		file, err := store.OpenItem(ctx, item)
		if err != nil {
			return nil, runtimeFileError(item.Name, err)
		}
		sealed, sealErr := supply.SealRuntime(ctx, item, file)
		if err := errors.Join(sealErr, file.Close()); err != nil {
			if sealed != nil {
				err = errors.Join(err, sealed.Close())
			}
			return nil, runtimeFileError(item.Name, err)
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
	return owned, nil
}

func runtimeFileError(name string, err error) error {
	var mismatch *supply.MismatchError
	if errors.Is(err, os.ErrNotExist) || errors.As(err, &mismatch) {
		return failure("runtime_missing", "the host lacks its client's verified gvisor runtime file "+name, err)
	}
	return err
}
