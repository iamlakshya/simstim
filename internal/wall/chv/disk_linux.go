//go:build linux

package chv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// SessionDiskName is the single writable raw disk inside a VM's data folder.
// The authoritative session manifest and supervisor lock live outside it.
const SessionDiskName = "disk.img"

// DiskInfo reports a complete disk. Created means this invocation published
// the new disk; the field stays true if a later durability check fails.
type DiskInfo struct {
	Bytes   int64 `json:"bytes"`
	Created bool  `json:"created"`
}

// DiskFormatter formats only the supplied fresh temporary descriptor.
// It must leave that descriptor open and must not change its pathname.
type DiskFormatter func(context.Context, *os.File) error

// EnsureDisk runs inside the chosen VMM uid's preparation jail. The caller
// holds its trusted supervisor lock outside the writable VM-data bind.
// It never formats, truncates, parses or removes an existing session disk.
func EnsureDisk(ctx context.Context, vmDir string, bytes int64, format DiskFormatter) (info DiskInfo, returned error) {
	if err := ctx.Err(); err != nil {
		return info, err
	}
	if bytes <= 0 {
		return info, fmt.Errorf("a session disk needs a positive byte size")
	}
	root, err := cachefs.OpenPrivateRoot(vmDir, false)
	if err != nil {
		return info, fmt.Errorf("open session disk folder: %w", err)
	}
	defer root.Close()
	if existing, present, err := existingDisk(root, bytes); present || err != nil {
		return existing, err
	}
	if format == nil {
		return info, fmt.Errorf("a new session disk needs the confined host formatter")
	}
	name, err := newDiskName()
	if err != nil {
		return info, err
	}
	file, err := cachefs.OpenFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return info, fmt.Errorf("create temporary session disk: %w", err)
	}
	defer func() {
		if _, err := cachefs.ValidateFile(root, name, file, 0o600); err == nil {
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				returned = errors.Join(returned, fmt.Errorf("remove temporary session disk: %w", err))
			}
		}
		if err := file.Close(); returned == nil && err != nil {
			returned = fmt.Errorf("close session disk: %w", err)
		}
	}()
	if err := file.Truncate(bytes); err != nil {
		return info, fmt.Errorf("size new session disk: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	if err := format(ctx, file); err != nil {
		if ctx.Err() != nil {
			return info, ctx.Err()
		}
		return info, fmt.Errorf("format new session disk: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	return finishNewDisk(ctx, root, name, file, bytes)
}

func finishNewDisk(ctx context.Context, root *os.Root, name string, file *os.File, bytes int64) (info DiskInfo, returned error) {
	if err := checkNewDisk(root, name, file, bytes); err != nil {
		return info, err
	}
	if err := file.Sync(); err != nil {
		return info, fmt.Errorf("sync new session disk: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	if err := publishDisk(root, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, present, readErr := existingDisk(root, bytes)
			if readErr != nil {
				return existing, readErr
			}
			if present {
				return existing, nil
			}
		}
		return info, fmt.Errorf("publish new session disk: %w", err)
	}
	info = DiskInfo{Bytes: bytes, Created: true}
	dir, err := root.Open(".")
	if err != nil {
		return info, fmt.Errorf("open session disk folder for sync: %w", err)
	}
	defer dir.Close()
	if err := cachefs.SyncDirectory(dir); err != nil {
		return info, fmt.Errorf("sync session disk publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	return info, nil
}

func existingDisk(root *os.Root, bytes int64) (DiskInfo, bool, error) {
	file, err := cachefs.OpenFile(root, SessionDiskName, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return DiskInfo{}, false, nil
	}
	if err != nil {
		return DiskInfo{}, false, fmt.Errorf("open existing session disk: %w", err)
	}
	defer file.Close()
	stat, err := cachefs.ValidateFile(root, SessionDiskName, file, 0o600)
	if err != nil {
		return DiskInfo{}, true, fmt.Errorf("check existing session disk: %w", err)
	}
	info := DiskInfo{Bytes: stat.Size()}
	if stat.Size() != bytes {
		return info, true, fmt.Errorf("existing session disk has %d bytes, but the request names %d; it was left unchanged", stat.Size(), bytes)
	}
	return info, true, nil
}

func newDiskName() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("name temporary session disk: %w", err)
	}
	return ".new-disk-" + hex.EncodeToString(nonce[:]), nil
}

func checkNewDisk(root *os.Root, name string, file *os.File, bytes int64) error {
	stat, err := cachefs.ValidateFile(root, name, file, 0o600)
	if err != nil {
		return fmt.Errorf("check formatted session disk identity: %w", err)
	}
	if stat.Size() != bytes {
		return fmt.Errorf("the new session disk changed size from %d to %d bytes", bytes, stat.Size())
	}
	var magic [2]byte
	if _, err := file.ReadAt(magic[:], 1024+56); err != nil {
		return fmt.Errorf("read new ext4 session disk superblock: %w", err)
	}
	if magic != [2]byte{0x53, 0xef} {
		return fmt.Errorf("the host formatter did not create an ext4 session disk")
	}
	return nil
}

func publishDisk(root *os.Root, name string) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open new session disk publication folder: %w", err)
	}
	defer dir.Close()
	if err := unix.Renameat2(int(dir.Fd()), name, int(dir.Fd()), SessionDiskName, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("rename new session disk without replacement: %w", err)
	}
	return nil
}
