//go:build linux

package slotlaunch

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// This private evidence owner is issued only while the actual adopted Source
// has positive whole closure. It retains real original kernel objects after
// data-role Close; a closed flag or cached exit result cannot stand in for it.
type nativeAllocationClosureV1 struct {
	keeper, init, group, peer *os.File
	identity                  cgroupIdentity
	once                      sync.Once
	err                       error
	released                  bool
}

// Caller holds Source.mu and has already observed its actual whole closure.
func captureNativeClosureV1(source *NativeSourceV1) (_ *nativeAllocationClosureV1, returned error) {
	if source == nil || !source.adopted || len(source.files) != 13 {
		return nil, refused("incomplete source cannot issue native closure evidence")
	}
	result := &nativeAllocationClosureV1{identity: source.identity}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, result.close())
		}
	}()
	var err error
	result.keeper, err = dupFile(source.files[0])
	if err != nil {
		return nil, err
	}
	result.init, err = dupFile(source.files[1])
	if err != nil {
		return nil, err
	}
	result.group, err = dupFile(source.files[2])
	if err != nil {
		return nil, err
	}
	if source.guestPeer != nil {
		result.peer, err = dupFile(source.guestPeer)
		if err != nil {
			return nil, err
		}
	}
	if !result.check() {
		return nil, refused("the actual original allocation closure changed during capture")
	}
	complete = true
	return result, nil
}

func (proof *nativeAllocationClosureV1) check() bool {
	if proof == nil || !pidExited(proof.keeper) || !pidExited(proof.init) || proof.peer != nil && !pidExited(proof.peer) || proof.group == nil {
		return false
	}
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(proof.group.Fd()), &stat) != nil || unix.Fstatfs(int(proof.group.Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || uint64(stat.Dev) != proof.identity.Dev || stat.Ino != proof.identity.Ino {
		return false
	}
	if stat.Nlink == 0 {
		return true
	}
	live, err := populated(proof.group)
	return err == nil && !live
}

func (proof *nativeAllocationClosureV1) close() error {
	if proof == nil {
		return nil
	}
	proof.once.Do(func() {
		for _, file := range []*os.File{proof.keeper, proof.init, proof.group, proof.peer} {
			if file != nil {
				proof.err = errors.Join(proof.err, file.Close())
			}
		}
		proof.released = true
	})
	return proof.err
}
