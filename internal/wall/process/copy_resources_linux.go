//go:build linux

package process

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/wall"
)

type copyMountNativeV1 struct {
	root, stage *os.File
	cleanupRoot *os.File
	ownedName   string
	identity    os.FileInfo
}
type copyStageNativeV1 struct {
	file     *os.File
	mount    *copyMountNativeV1
	name     string
	identity os.FileInfo
}
type copyOwnerNativeV1 struct {
	runtime      *sandbox
	conn         *net.UnixConn
	mu           sync.Mutex
	closed       atomic.Bool
	ready        atomic.Bool
	mounts       map[uint32]*copyMountNativeV1
	active       *copyStageNativeV1
	publisher    int
	interruptMu  sync.Mutex
	interruptErr error
	closeErr     error
}

// NativeCopyOwnerV1 accepts only this package's concrete started sandbox and
// its exact Start-returned connection. Other drivers obtain no process grant.
func NativeCopyOwnerV1(runtime wall.Sandbox, connection net.Conn) (*CopyOwnerV1, error) {
	s, ok := runtime.(*sandbox)
	if !ok {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.copy == nil || s.copy.native == nil || s.copy.native.closed.Load() || !s.copy.native.ready.Load() || s.cmd == nil || s.closed || s.pidfd < 0 || s.link != connection {
		return nil, setupError("the process runtime has no live original copy owner", nil)
	}
	return s.copy, nil
}

func receiveCopyResourcesV1(owner *CopyOwnerV1, request Request) error {
	native := owner.native
	seen := map[uint32]bool{}
	for {
		data, files, err := copyMessage(native.conn, 4, 2)
		if err != nil {
			return err
		}
		index := binary.LittleEndian.Uint32(data)
		if index == copyEndIndex {
			if len(files) != 0 || native.mounts[copyRootIndex] == nil {
				return fmt.Errorf("incomplete original factory resources")
			}
			for index := range native.mounts {
				if !seen[index] {
					return fmt.Errorf("original factory stage was not confirmed by initializer")
				}
			}
			native.ready.Store(true)
			return nil
		}
		if len(files) != 2 || seen[index] || index != copyRootIndex && (uint64(index) >= uint64(len(request.Mounts)) || native.mounts[index] == nil) {
			for _, file := range files {
				_ = file.Close()
			}
			return fmt.Errorf("unexpected original copy mount")
		}
		seen[index] = true
		if index == copyRootIndex {
			mount := &copyMountNativeV1{root: files[0], stage: files[1]}
			native.mounts[index] = mount
			rootInfo, rootErr := mount.root.Stat()
			mount.identity, err = mount.stage.Stat()
			if rootErr != nil || err != nil || !rootInfo.IsDir() || !mount.identity.IsDir() || mount.identity.Mode().Perm()&0077 != 0 {
				return errors.Join(rootErr, err, fmt.Errorf("fresh copy root or stage is invalid"))
			}
			continue
		}
		expected := native.mounts[index]
		rootInfo, rootErr := files[0].Stat()
		stageInfo, stageErr := files[1].Stat()
		original, originalErr := expected.root.Stat()
		closeErr := errors.Join(files[0].Close(), files[1].Close())
		if rootErr != nil || stageErr != nil || originalErr != nil || !os.SameFile(rootInfo, original) || !os.SameFile(stageInfo, expected.identity) {
			return errors.Join(rootErr, stageErr, originalErr, closeErr, fmt.Errorf("initializer changed original factory handles"))
		}
		if closeErr != nil {
			return closeErr
		}
		var name [39]byte
		if err := copyOrdinaryRead(native.conn, name[:]); err != nil {
			return err
		}
		if name[38] != 0 || string(name[:38]) != expected.ownedName {
			return fmt.Errorf("initializer changed original factory cleanup identity")
		}
	}
}

func (o *copyOwnerNativeV1) interrupt() {
	if o == nil {
		return
	}
	o.interruptMu.Lock()
	defer o.interruptMu.Unlock()
	if !o.closed.Swap(true) {
		o.interruptErr = o.conn.Close()
	}
}

// nativeExit runs only after the original launcher/init exit. It preserves
// native publishers and staging objects on uncertainty, so a retry cannot
// convert a previous unlink or descriptor failure into cleanup success.
func (o *copyOwnerNativeV1) nativeExit() error {
	if o == nil {
		return nil
	}
	o.interrupt()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.interruptMu.Lock()
	interruptErr := o.interruptErr
	o.interruptMu.Unlock()
	if err := errors.Join(o.closeErr, interruptErr); err != nil {
		return err
	}
	if o.publisher >= 0 {
		if err := waitInitExit(o.publisher); err != nil {
			return err
		}
		if err := unix.Close(o.publisher); err != nil {
			o.closeErr = err
			return err
		}
		o.publisher = -1
	}
	if o.active != nil {
		stage := o.active
		if err := copyUnlinkOwned(stage.mount.stage, stage.name, stage.identity, false); err != nil {
			return err
		}
		if err := stage.file.Close(); err != nil {
			o.closeErr = err
			return err
		}
		o.active = nil
	}
	for index, mount := range o.mounts {
		if mount.ownedName != "" {
			if err := copyUnlinkOwned(mount.cleanupRoot, mount.ownedName, mount.identity, true); err != nil {
				return err
			}
			mount.ownedName = ""
		}
		for _, field := range []**os.File{&mount.stage, &mount.root, &mount.cleanupRoot} {
			if *field == nil {
				continue
			}
			if err := (*field).Close(); err != nil {
				o.closeErr = errors.Join(o.closeErr, err)
			} else {
				*field = nil
			}
		}
		if o.closeErr != nil {
			return o.closeErr
		}
		delete(o.mounts, index)
	}
	return nil
}

func copyUnlinkOwned(parent *os.File, name string, expected os.FileInfo, directory bool) error {
	raw, err := parent.SyscallConn()
	if err != nil {
		return err
	}
	var operation error
	err = raw.Control(func(fd uintptr) {
		var info unix.Stat_t
		if e := unix.Fstatat(int(fd), name, &info, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			if !errors.Is(e, unix.ENOENT) {
				operation = e
			}
			return
		}
		opened, e := unix.Openat(int(fd), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			operation = e
			return
		}
		file := os.NewFile(uintptr(opened), "owned-copy-cleanup")
		current, e := file.Stat()
		closeErr := file.Close()
		if e != nil || !os.SameFile(expected, current) {
			operation = errors.Join(e, closeErr, fmt.Errorf("owned copy staging identity changed"))
			return
		}
		operation = closeErr
		flags := 0
		if directory {
			flags = unix.AT_REMOVEDIR
		}
		operation = errors.Join(operation, unix.Unlinkat(int(fd), name, flags))
	})
	return errors.Join(err, operation)
}
