package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Lock is the client-owned images.lock document of isolation spec 20.8.
type Lock struct {
	Version int   `toml:"version"`
	Images  []Pin `toml:"image"`
}

// Pin records manifest authority resolved on the laptop, never a host digest.
type Pin struct {
	Ref       string        `toml:"ref"`
	Digest    string        `toml:"digest"`
	PinnedAt  time.Time     `toml:"pinned_at"`
	SignedBy  string        `toml:"signed_by"`
	Policy    string        `toml:"policy"`
	HostID    string        `toml:"host_id"`
	Platforms []PlatformPin `toml:"platform"`
}

// PlatformPin records the first conversion's check value for one architecture.
type PlatformPin struct {
	Arch       string `toml:"arch"`
	Digest     string `toml:"digest"`
	RootHash   string `toml:"root_hash"`
	DataBlocks int64  `toml:"data_blocks"`
	RootFrom   string `toml:"root_from"`
	SignedBy   string `toml:"signed_by"`
	Policy     string `toml:"policy"`
}

func (l *Lock) validate() error {
	if l.Version != 1 {
		return fmt.Errorf("images.lock has an unsupported version")
	}
	seen := map[string]bool{}
	for _, pin := range l.Images {
		if _, err := ParseReference(pin.Ref); err != nil {
			return err
		}
		if seen[pin.Ref] || !ValidDigest(pin.Digest) || pin.PinnedAt.IsZero() {
			return fmt.Errorf("images.lock has an invalid or repeated image pin")
		}
		seen[pin.Ref] = true
		arches := map[string]bool{}
		for _, platform := range pin.Platforms {
			if arches[platform.Arch] || platform.Arch != "amd64" && platform.Arch != "arm64" || !ValidDigest(platform.Digest) {
				return fmt.Errorf("images.lock has an invalid platform pin")
			}
			arches[platform.Arch] = true
			if (platform.RootHash == "") != (platform.DataBlocks == 0) || platform.RootHash != "" && !supply.IsHex64(platform.RootHash) || platform.DataBlocks < 0 || platform.DataBlocks > MaxUncompressed/4096 {
				return fmt.Errorf("images.lock has an invalid root check")
			}
		}
	}
	return nil
}

// UpdateLock rereads images.lock under its adjacent exclusive file lock, then
// atomically replaces it. The callback must derive pins only on the laptop.
func UpdateLock(ctx context.Context, dir string, change func(*Lock) error) error {
	root, err := openPrivateRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	s := &Store{root: root}
	lease, err := s.lock(ctx, "images.lock.lock", false)
	if err != nil {
		return err
	}
	defer lease.Close()
	lock := Lock{Version: 1}
	file, err := s.regular("images.lock", os.O_RDONLY, 0o600)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, MaxManifest+1))
		file.Close()
		if readErr != nil {
			return readErr
		}
		if int64(len(data)) > MaxManifest {
			return fmt.Errorf("images.lock exceeds 4 MiB")
		}
		metadata, err := toml.Decode(string(data), &lock)
		if err != nil {
			return fmt.Errorf("read images.lock: %w", err)
		}
		if len(metadata.Undecoded()) != 0 {
			return fmt.Errorf("images.lock has unknown fields")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := lock.validate(); err != nil {
		return err
	}
	if err := change(&lock); err != nil {
		return err
	}
	if err := lock.validate(); err != nil {
		return err
	}
	var data bytes.Buffer
	fmt.Fprintf(&data, "# Written by %s. Image manifest digests are client-owned pins.\n", brand.Name)
	if err := toml.NewEncoder(&data).Encode(lock); err != nil {
		return err
	}
	if int64(data.Len()) > MaxManifest {
		return fmt.Errorf("images.lock exceeds 4 MiB")
	}
	nonce, err := randomName()
	if err != nil {
		return err
	}
	name := ".images-" + nonce
	out, err := s.regular(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := out.Write(data.Bytes())
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(name, "images.lock"); err != nil {
		return err
	}
	folder, err := root.Open(".")
	if err != nil {
		return err
	}
	defer folder.Close()
	return cachefs.SyncDirectory(folder)
}
