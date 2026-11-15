//go:build linux

package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/wall"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
)

const firecrackerConfigName = "fc.json"

type protectedBootConfig struct {
	root *os.Root
	file *os.File
	ref  cachefs.Reference
	path string
}

func (c *protectedBootConfig) Close() error {
	if c == nil {
		return nil
	}
	return errors.Join(c.file.Close(), c.root.Close())
}

func (c *protectedBootConfig) check() error {
	return c.ref.CheckFile(c.root, firecrackerConfigName, c.file, 0o444)
}

// prepareFirecrackerConfig writes only derived boot data outside the writable
// VM-data bind. The caller holds this session's native runtime lease. The file
// never supplies runtime, image, process or original-operation authority.
func prepareFirecrackerConfig(ctx context.Context, spec wall.Spec) (_ *protectedBootConfig, returned error) {
	api, vsock := vmSocketPaths(vmDataDir(spec))
	_, data, _, err := fc.Command(spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
	if err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, root.Close())
		}
	}()
	// A stopped controller may replace its old generated data, but never a
	// symlink, multiply linked object or file with another owner/role mode.
	previous, err := cachefs.OpenFile(root, firecrackerConfigName, os.O_RDONLY, 0o444)
	if err == nil {
		if err := previous.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect previous Firecracker boot data: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("name Firecracker boot data: %w", err)
	}
	name := ".fc-" + hex.EncodeToString(nonce[:])
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, file.Close())
		}
		if !published {
			returned = errors.Join(returned, root.Remove(name))
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n, err := file.Write(data); err != nil {
		return nil, fmt.Errorf("write Firecracker boot data: %w", err)
	} else if n != len(data) {
		return nil, fmt.Errorf("write Firecracker boot data: %w", io.ErrShortWrite)
	}
	if err := errors.Join(file.Chmod(0o444), file.Sync()); err != nil {
		return nil, fmt.Errorf("seal Firecracker boot data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := root.Rename(name, firecrackerConfigName); err != nil {
		return nil, fmt.Errorf("publish Firecracker boot data: %w", err)
	}
	published = true
	ref, err := cachefs.CaptureFile(root, firecrackerConfigName, file, 0o444)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	if err := errors.Join(cachefs.SyncDirectory(directory), directory.Close()); err != nil {
		return nil, fmt.Errorf("sync Firecracker boot data: %w", err)
	}
	return &protectedBootConfig{root: root, file: file, ref: ref, path: filepath.Join(spec.SessionDir, firecrackerConfigName)}, nil
}
