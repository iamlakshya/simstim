package image

import (
	"context"
	"errors"
	"os"
)

// CacheBlob downloads a verified layer into the laptop's private blob cache.
// It returns an open verified descriptor; registry credentials stay local.
func CacheBlob(ctx context.Context, dir string, registry *Registry, ref Reference, layer Layer) (*os.File, error) {
	if !ValidDigest(layer.Digest) || layer.Size < 0 || layer.Size > MaxCompressed {
		return nil, badLayer("invalid layer cache descriptor")
	}
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	for _, name := range []string{"blobs", "blobs/sha256"} {
		if err := store.mkdir(name); err != nil {
			return nil, err
		}
	}
	lease, err := store.lock(ctx, "incoming/"+layer.Digest[7:]+".lock", false)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	name := "blobs/sha256/" + layer.Digest[7:]
	if file, err := store.regular(name, os.O_RDONLY, 0o444); err == nil {
		if err := checkBlob(file, layer); err == nil {
			return file, nil
		}
		file.Close()
		return nil, ownershipError("cached blob is damaged and has no producer receipt for automatic replacement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	nonce, err := randomName()
	if err != nil {
		return nil, err
	}
	temporary := "incoming/" + layer.Digest[7:] + "-" + nonce + ".part"
	file, err := store.regular(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	created, err := store.captureCreated(temporary, file, 0o600)
	if err != nil {
		file.Close()
		return nil, err
	}
	mode := os.FileMode(0o600)
	defer func() { _ = store.cleanupCreated(created, mode) }()
	if err := registry.Blob(ctx, ref, layer, file, nil); err != nil {
		cleanupErr := store.cleanupCreated(created, mode)
		file.Close()
		return nil, errors.Join(err, cleanupErr)
	}
	if err := file.Chmod(0o444); err != nil {
		cleanupErr := store.cleanupCreated(created, mode)
		file.Close()
		return nil, errors.Join(err, cleanupErr)
	}
	mode = 0o444
	if err := file.Sync(); err != nil {
		cleanupErr := store.cleanupCreated(created, mode)
		file.Close()
		return nil, errors.Join(err, cleanupErr)
	}
	if err := store.root.Rename(temporary, name); err != nil {
		cleanupErr := store.cleanupCreated(created, mode)
		file.Close()
		return nil, errors.Join(err, cleanupErr)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return store.regular(name, os.O_RDONLY, 0o444)
}
