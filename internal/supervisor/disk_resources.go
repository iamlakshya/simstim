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
	mu      sync.Mutex
	volume  *os.Root
	roots   []*os.Root
	process bool
	limit   int64
	release func() error
	closed  bool
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
	if r.closed || r.volume == nil || r.limit < 0 || r.process && len(r.roots) == 0 || requestStop == nil {
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
	if r.closed {
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
	if r.release != nil {
		if err := r.release(); err != nil {
			return err // A live runtime still owns its lease; cleanup must finish.
		}
	}
	r.closed = true
	var result error
	for index := len(r.roots) - 1; index >= 0; index-- {
		result = errors.Join(result, r.roots[index].Close())
	}
	if r.volume != nil {
		result = errors.Join(result, r.volume.Close())
	}
	r.roots, r.volume = nil, nil
	return result
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
func pinOwnedDiskRootV1(volume *os.Root, session, name string) (*os.Root, error) {
	relative, err := filepath.Rel(session, name)
	if err != nil || !files.Relative(filepath.ToSlash(relative)) {
		return nil, diskFailure("disk_measure", "a factory-owned writable root leaves its private session volume")
	}
	pin, err := files.PinMount(name, false)
	if err != nil {
		return nil, wallError("cannot pin a factory-owned writable root without links", err)
	}
	defer pin.Close()
	want, err := pin.Stat()
	if err != nil {
		return nil, wallError("cannot identify the factory-owned writable root", err)
	}
	root, err := volume.OpenRoot(relative)
	if err != nil {
		return nil, wallError("cannot open the pinned writable root", err)
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, wallError("cannot identify the opened writable root", err)
	}
	got, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(want, got) {
		root.Close()
		return nil, diskFailure("disk_measure", "the factory-owned writable root changed while it was being pinned")
	}
	return root, nil
}
