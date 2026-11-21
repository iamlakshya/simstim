//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// CopyForkDiskToV1 borrows the SAME root-issued slot disk and writes a fresh
// child-creator descriptor. The original source is retained and checked before
// and after the copy. The caller owns the guest freeze lease and real child
// reservation/OP; a supplied pathname, manifest or empty-file DTO grants none.
// This method issues no slot allocation or native cold-start permission.
func (s *NativeSourceV1) CopyForkDiskToV1(ctx context.Context, target *os.File) (returned error) {
	if s == nil || ctx == nil || target == nil {
		return refused("the slot fork has no original source or actual child file")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, s.request); err != nil {
		return err
	}
	if s.guestPeer == nil || !pidLive(s.guestPeer) {
		return refused("the original slot fork VMM peer ended")
	}
	original, err := cachefs.ValidateDescriptor(target, 0o600)
	if err != nil || original.Size() != 0 {
		return refused("the child fork output is not a fresh private single-link descriptor")
	}
	disk := s.files[9]
	source, err := disk.Stat()
	if err != nil || source.Size() != s.request.DiskMaxGiB*(1<<30) {
		return refused("the original slot fork disk changed its admitted length")
	}
	if os.SameFile(source, original) {
		return refused("the slot fork output aliases its original source disk")
	}
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	joined := make(chan struct{})
	stop := context.AfterFunc(s.owner, func() { defer close(joined); cancel() })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	defer func() {
		if returned == nil {
			return
		}
		// The actual creator still owns the original empty output. Release
		// unfinished physical blocks only while that SAME private inode is
		// present; a substituted or linked file is preserved for inspection.
		if current, err := cachefs.ValidateDescriptor(target, 0o600); err == nil && os.SameFile(current, original) {
			returned = errors.Join(returned, target.Truncate(0))
		}
	}()
	err = unix.IoctlFileClone(int(target.Fd()), int(disk.Fd()))
	if err != nil {
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) {
			return err
		}
		if err := target.Truncate(0); err != nil {
			return err
		}
		if err := copySparseForkDiskV1(owned, disk, target, source.Size()); err != nil {
			return err
		}
	}
	if err := errors.Join(owned.Err(), target.Sync(), s.check(ctx, s.request)); err != nil {
		return err
	}
	current, err := cachefs.ValidateDescriptor(target, 0o600)
	if err != nil || !os.SameFile(current, original) || current.Size() != source.Size() {
		return refused("the actual child fork output changed identity or length")
	}
	if !pidLive(s.guestPeer) {
		return refused("the original slot fork VMM peer ended during its copy")
	}
	return nil
}

func copySparseForkDiskV1(ctx context.Context, source, target *os.File, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := target.Truncate(size); err != nil {
		return err
	}
	// Open the SAME held descriptor through procfs for an independent seek
	// cursor. No guest path, saved path or source filename participates.
	// Seeking a DUP would change the VMM's own open-file-description cursor.
	cursor, err := os.Open("/proc/self/fd/" + strconv.Itoa(int(source.Fd())))
	if err != nil {
		return err
	}
	defer cursor.Close()
	left, err := source.Stat()
	if err != nil {
		return err
	}
	right, err := cursor.Stat()
	if err != nil || !os.SameFile(left, right) {
		return refused("the slot fork seek cursor is not its original held disk")
	}
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := unix.Seek(int(cursor.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			return nil
		}
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
			return copyForkExtentV1(ctx, source, target, offset, size, buffer, true)
		}
		if err != nil {
			return err
		}
		if data >= size {
			return nil
		}
		hole, err := unix.Seek(int(cursor.Fd()), data, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		if hole <= data {
			return refused("the original fork disk reported an invalid sparse extent")
		}
		hole = min(hole, size)
		if err := copyForkExtentV1(ctx, source, target, data, hole, buffer, false); err != nil {
			return err
		}
		offset = hole
	}
	return nil
}

func copyForkExtentV1(ctx context.Context, source, target *os.File, from, to int64, buffer []byte, skipZero bool) error {
	for offset := from; offset < to; {
		if err := ctx.Err(); err != nil {
			return err
		}
		part := buffer[:min(int64(len(buffer)), to-offset)]
		n, err := source.ReadAt(part, offset)
		if err != nil && !(errors.Is(err, io.EOF) && n == len(part)) {
			return err
		}
		if n != len(part) {
			return io.ErrUnexpectedEOF
		}
		zero := skipZero
		if skipZero {
			for _, value := range part {
				if value != 0 {
					zero = false
					break
				}
			}
		}
		if !zero {
			if written, err := target.WriteAt(part, offset); err != nil {
				return err
			} else if written != len(part) {
				return io.ErrShortWrite
			}
		}
		offset += int64(len(part))
	}
	return nil
}
