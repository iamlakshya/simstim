//go:build linux

package chv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/wall"
)

const vmSpecName = "chv.json"

// The canonical ID is at most32 bytes and option-safe socket paths cap the
// session directory below108 bytes. Even JSON escaping plus fixed fields
// fits1024 bytes. Runtime/image authority is never copied into this layout.
const maxVMSpec = 1024

type savedVM struct {
	Version    int    `json:"version"`
	UIDMode    string `json:"uid_mode"`
	ID         string `json:"id"`
	SessionDir string `json:"session_dir"`
	DiskMax    int64  `json:"disk_max"`
}

func readVMRecord(dir string) (savedVM, error) {
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		return savedVM{}, err
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, vmSpecName, os.O_RDONLY, 0o600)
	if err != nil {
		return savedVM{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxVMSpec+1))
	if err != nil {
		return savedVM{}, fmt.Errorf("read saved microvm spec: %w", err)
	}
	if len(data) > maxVMSpec {
		return savedVM{}, fmt.Errorf("saved microvm spec exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var saved savedVM
	if err := decoder.Decode(&saved); err != nil {
		return savedVM{}, fmt.Errorf("decode saved microvm spec: %w", err)
	}
	var more any
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return savedVM{}, fmt.Errorf("saved microvm spec has trailing content")
	}
	if saved.Version != 1 || saved.UIDMode != "login" && saved.UIDMode != "subuid" {
		return savedVM{}, fmt.Errorf("unknown saved microvm spec version or identity mode")
	}
	return saved, nil
}

func (d *Driver) prepare(ctx context.Context, spec wall.Spec, create bool, mode string) (wall.Sandbox, error) {
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lease, err := acquireVMLease(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		lease.Close()
		return nil, err
	}
	if create {
		if err := writeVMSpec(root, spec, mode); err != nil {
			lease.Close()
			return nil, err
		}
		if err := cachefs.MakePrivateDir(root, "vm"); err != nil {
			lease.Close()
			return nil, err
		}
	}
	return newSandbox(d, spec, lease, mode, ctx), nil
}

func acquireVMLease(ctx context.Context, root *os.Root) (*os.File, error) {
	lease, err := cachefs.OpenFile(root, "chv.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			lease.Close()
			return nil, err
		}
		held, err := cachefs.TryLock(lease, cachefs.Exclusive)
		if err != nil {
			lease.Close()
			return nil, err
		}
		if held {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			lease.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if _, err := cachefs.ValidateFile(root, "chv.lock", lease, 0o600); err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
}

func writeVMSpec(root *os.Root, spec wall.Spec, mode string) error {
	if _, err := root.Lstat(vmSpecName); err == nil {
		return fmt.Errorf("this session already has a microvm specification")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect microvm specification: %w", err)
	}
	data, err := json.Marshal(savedVM{Version: 1, UIDMode: mode, ID: spec.ID, SessionDir: spec.SessionDir, DiskMax: spec.DiskMax})
	if err != nil {
		return fmt.Errorf("encode microvm specification: %w", err)
	}
	if len(data) > maxVMSpec {
		return fmt.Errorf("microvm specification exceeds its bound")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("name microvm specification: %w", err)
	}
	name := ".chv-" + hex.EncodeToString(nonce[:])
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write microvm specification: %w", err)
	}
	if err := root.Rename(name, vmSpecName); err != nil {
		return fmt.Errorf("publish microvm specification: %w", err)
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return cachefs.SyncDirectory(dir)
}

func vmDataDir(spec wall.Spec) string { return filepath.Join(spec.SessionDir, "vm") }
