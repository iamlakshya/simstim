package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/files"
)

// diskResourcesV1 is issued only by this package's actual runtime factories.
// No public constructor, serialised path or caller-supplied root can mint it.
// The watcher borrows its roots; its owner joins the watcher before Close.
type diskResourcesV1 struct {
	mu       sync.Mutex
	volume   *os.Root
	roots    []*os.Root
	checks   []*os.Root
	files    []*os.File
	process  bool
	limit    int64
	release  func() error
	closed   bool
	closing  bool
	closeErr error
}

type ownedDiskRuntimeV1 interface {
	ownedDiskResourcesV1() *diskResourcesV1
}

func (r *diskResourcesV1) options(requestStop func(error)) (diskOptions, error) {
	if r == nil {
		return diskOptions{}, diskFailure("disk_measure", "the runtime factory supplied no owned disk resources")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.closing || r.closeErr != nil || r.volume == nil || r.limit < 0 || r.process && len(r.roots) == 0 || requestStop == nil {
		return diskOptions{}, diskFailure("disk_measure", "the runtime factory's owned disk resources are incomplete or closed")
	}
	if r.process {
		if err := files.ValidateDiskRoots(r.volume, r.roots...); err != nil {
			return diskOptions{}, wallError("cannot validate the factory-owned disk roots", err)
		}
	}
	return diskOptions{volume: r.volume, roots: slices.Clone(r.roots), process: r.process, limit: r.limit, requestStop: requestStop}, nil
}

// start fences a discarded prepared runtime against a later Start. Closing
// waits for an in-progress bounded Start; cancellation remains its owner's.
func (r *diskResourcesV1) start(ctx context.Context, run func(context.Context) (net.Conn, error)) (net.Conn, error) {
	if r == nil {
		return nil, diskFailure("disk_measure", "the runtime has no factory-owned disk resources")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.closing || r.volume == nil || r.closeErr != nil {
		return nil, diskFailure("disk_measure", "the prepared runtime's disk resources have already been released")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return run(ctx)
}

// Close releases only this factory's metadata roots and prepared lease. It
// removes no data. Its owner must cancel and join the watcher before calling.
func (r *diskResourcesV1) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closing = true
	if r.closeErr != nil {
		return r.closeErr
	}
	if r.release != nil {
		if err := r.release(); err != nil {
			return err
		}
		r.release = nil
	}
	for index := len(r.files) - 1; index >= 0; index-- {
		if r.files[index] == nil {
			continue
		}
		if err := r.files[index].Close(); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		} else {
			r.files[index] = nil
		}
	}
	for _, roots := range [][]*os.Root{r.checks, r.roots} {
		for index := len(roots) - 1; index >= 0; index-- {
			if roots[index] == nil {
				continue
			}
			if err := closeCheckedRootV1(roots[index]); err != nil {
				r.closeErr = errors.Join(r.closeErr, err)
			} else {
				roots[index] = nil
			}
		}
	}
	if r.volume != nil {
		if err := closeCheckedRootV1(r.volume); err != nil {
			r.closeErr = errors.Join(r.closeErr, err)
		} else {
			r.volume = nil
		}
	}
	if r.closeErr != nil {
		return r.closeErr
	}
	r.files, r.checks, r.roots = nil, nil, nil
	r.closed = true
	return nil
}

func closeCheckedRootV1(root *os.Root) error {
	_, statErr := root.Stat(".")
	return errors.Join(statErr, root.Close())
}

func pinDiskVolumeV1(name string) (*os.Root, error) {
	root, err := cachefs.OpenPrivateRoot(name, false)
	if err != nil {
		return nil, wallError("cannot pin the runtime factory's private disk volume", err)
	}
	return root, nil
}

// pinOwnedDiskRootV1 joins the existing no-follow directory descriptor policy
// to a rooted view of the SAME object. It never follows a grant or link tree.
func (resources *diskResourcesV1) pinOwnedRootV1(volume *os.Root, session, name string) (*os.Root, error) {
	relative, err := filepath.Rel(session, name)
	if err != nil || !files.Relative(filepath.ToSlash(relative)) {
		return nil, diskFailure("disk_measure", "a factory-owned writable root leaves its private session volume")
	}
	pin, err := files.PinMount(name, false)
	if err != nil {
		return nil, wallError("cannot pin a factory-owned writable root without links", err)
	}
	resources.files = append(resources.files, pin)
	want, err := pin.Stat()
	if err != nil {
		return nil, wallError("cannot identify the factory-owned writable root", err)
	}
	root, err := volume.OpenRoot(relative)
	if err != nil {
		return nil, wallError("cannot open the pinned writable root", err)
	}
	resources.checks = append(resources.checks, root)
	opened, err := root.Open(".")
	if err != nil {
		return root, wallError("cannot identify the opened writable root", err)
	}
	resources.files = append(resources.files, opened)
	got, statErr := opened.Stat()
	if statErr != nil || !os.SameFile(want, got) {
		return root, diskFailure("disk_measure", "the factory-owned writable root changed while it was being pinned")
	}
	return root, nil
}
