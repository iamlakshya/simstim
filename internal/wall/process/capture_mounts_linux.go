//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// CaptureMountPlanV1 pins every entry of the canonical process plan exactly
// once. The returned identities describe those same descriptors. A consumer
// must mount the descriptors, rather than reopen the Host strings. The caller
// owns every returned file, including after a later namespace start fails.
func CaptureMountPlanV1(ctx context.Context, plan []wall.Mount) (captured []wall.Mount, owned []*os.File, returned error) {
	defer func() {
		if returned != nil {
			for _, file := range owned {
				returned = errors.Join(returned, file.Close())
			}
			captured, owned = nil, nil
		}
	}()
	captured = slices.Clone(plan)
	for index, mount := range plan {
		if err := ctx.Err(); err != nil {
			return nil, owned, err
		}
		if !filepath.IsAbs(mount.Host) || filepath.Clean(mount.Host) != mount.Host || !validGuest(mount.Guest) {
			return nil, owned, fmt.Errorf("the process mount plan contains a non-canonical source or destination")
		}
		var file *os.File
		var err error
		if mount.Identity != "" {
			file, err = files.PinGrant(mount)
		} else {
			file, err = pinSource(mount.Host)
		}
		if err != nil {
			return nil, owned, err
		}
		info, err := file.Stat()
		if err != nil {
			return nil, owned, errors.Join(err, file.Close())
		}
		if info.Mode().IsRegular() {
			// A readonly mount grants reads. Capture that actual right through
			// the pinned object, rather than treating O_PATH as a read grant
			// or reopening the source's possibly replaced Host pathname.
			fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(int(file.Fd())), unix.O_RDONLY|unix.O_CLOEXEC, 0)
			if err != nil {
				return nil, owned, errors.Join(err, file.Close())
			}
			readable := os.NewFile(uintptr(fd), "captured-readable-mount")
			got, statErr := readable.Stat()
			closeErr := file.Close()
			if statErr != nil || closeErr != nil || !os.SameFile(info, got) || mount.Write {
				return nil, owned, errors.Join(fmt.Errorf("the regular mount lost its declared readonly source"), statErr, closeErr, readable.Close())
			}
			file = readable
		}
		owned = append(owned, file)
		identity, err := files.Identity(file)
		if err != nil {
			return nil, owned, err
		}
		captured[index].Identity = identity
	}
	if err := ctx.Err(); err != nil {
		return nil, owned, err
	}
	return captured, owned, nil
}
