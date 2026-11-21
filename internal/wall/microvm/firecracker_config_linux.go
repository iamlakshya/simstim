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
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/wall"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
)

const firecrackerConfigName = "fc.json"

type protectedBootConfig struct {
	closeMu                    sync.Mutex
	root                       *os.Root
	file, previous, directory  *os.File
	ref, temporaryRef          cachefs.Reference
	path, temporary            string
	mode                       os.FileMode
	retirements                map[*os.File]*microVMFileRetirementV2
	diagnostic, rootCloseError error
}

func (c *protectedBootConfig) Close() error {
	if c == nil {
		return nil
	}
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.retirements == nil {
		c.retirements = make(map[*os.File]*microVMFileRetirementV2)
	}
	if c.temporary != "" {
		if c.file == nil || !c.temporaryRef.Known() || c.root == nil {
			return fmt.Errorf("the generated boot config still has an unknown original temporary owner")
		}
		if err := c.temporaryRef.CheckCreatedFile(c.root, c.temporary, c.file, c.mode); err != nil {
			return err
		}
		if err := c.root.Remove(c.temporary); err != nil {
			return err
		}
		c.temporary = ""
	}
	for _, file := range []**os.File{&c.directory, &c.previous, &c.file} {
		if err := closeMicroVMFileV2(file, c.retirements, &c.diagnostic); err != nil {
			return err
		}
	}
	if c.rootCloseError != nil {
		return c.rootCloseError
	}
	if c.root != nil {
		if err := c.root.Close(); err != nil {
			c.rootCloseError = err // No descriptor-close inference for os.Root.
			return err
		}
		c.root = nil
	}
	return nil
}

func (c *protectedBootConfig) diagnosticsV3() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.diagnostic
}

func (c *protectedBootConfig) check() error {
	return c.ref.CheckFile(c.root, firecrackerConfigName, c.file, 0o444)
}

// The original bootFiles caller adopts this SAME partial owner before checking
// any error. Derived data grants no runtime/image/process/operation authority.
func prepareFirecrackerConfig(ctx context.Context, spec wall.Spec) (owner *protectedBootConfig, returned error) {
	owner = &protectedBootConfig{mode: 0o600, retirements: make(map[*os.File]*microVMFileRetirementV2)}
	api, vsock := vmSocketPaths(vmDataDir(spec))
	build := fc.Command
	if spec.Driver == "nitro" {
		build = fc.NitroCommand
	}
	_, data, _, err := build(spec, fc.Paths{VMM: "/proc/self/fd/3", Kernel: "/proc/self/fd/4", Tools: "/proc/self/fd/5", Image: "/proc/self/fd/6", Agents: "/proc/self/fd/7", Session: "/proc/self/fd/8", Config: filepath.Join(spec.SessionDir, firecrackerConfigName), API: api, Vsock: vsock})
	if err != nil {
		return owner, err
	}
	owner.root, err = cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return owner, err
	}
	owner.previous, err = cachefs.OpenFile(owner.root, firecrackerConfigName, os.O_RDONLY, 0o444)
	if err == nil {
		if err := closeMicroVMFileV2(&owner.previous, owner.retirements, &owner.diagnostic); err != nil {
			return owner, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return owner, fmt.Errorf("inspect previous Firecracker boot data: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return owner, fmt.Errorf("name Firecracker boot data: %w", err)
	}
	name := ".fc-" + hex.EncodeToString(nonce[:])
	owner.file, err = cachefs.OpenFile(owner.root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, owner.mode)
	if owner.file != nil {
		owner.temporary = name // Adopt before reference capture or any later error.
	}
	if err != nil {
		return owner, err
	}
	owner.temporaryRef, err = cachefs.CaptureFile(owner.root, name, owner.file, owner.mode)
	if err != nil {
		return owner, err
	}
	if err := ctx.Err(); err != nil {
		return owner, err
	}
	if n, err := owner.file.Write(data); err != nil {
		return owner, fmt.Errorf("write Firecracker boot data: %w", err)
	} else if n != len(data) {
		return owner, fmt.Errorf("write Firecracker boot data: %w", io.ErrShortWrite)
	}
	if err := owner.file.Chmod(0o444); err != nil {
		return owner, fmt.Errorf("seal Firecracker boot data: %w", err)
	}
	owner.mode = 0o444
	if err := owner.file.Sync(); err != nil {
		return owner, fmt.Errorf("sync Firecracker boot data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return owner, err
	}
	if err := owner.root.Rename(name, firecrackerConfigName); err != nil {
		return owner, fmt.Errorf("publish Firecracker boot data: %w", err)
	}
	owner.temporary = ""
	owner.path = filepath.Join(spec.SessionDir, firecrackerConfigName)
	owner.ref, err = cachefs.CaptureFile(owner.root, firecrackerConfigName, owner.file, 0o444)
	if err != nil {
		return owner, err
	}
	owner.directory, err = owner.root.Open(".")
	if err != nil {
		return owner, err
	}
	if err := cachefs.SyncDirectory(owner.directory); err != nil {
		return owner, fmt.Errorf("sync Firecracker boot data: %w", err)
	}
	if err := closeMicroVMFileV2(&owner.directory, owner.retirements, &owner.diagnostic); err != nil {
		return owner, fmt.Errorf("close Firecracker boot directory: %w", err)
	}
	return owner, nil
}
