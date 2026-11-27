//go:build linux

package process

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const cgroupReadBoundV1 = 8192

// processCgroupV1 is created by Start in the supervisor's OWN kernel-reported
// cgroup. Its child is never selected by an ID, a manifest path or a callback.
// clone3 receives this same child's descriptor before the launcher runs.
type processCgroupV1 struct {
	root, parent, child *os.File
	rootRef, parentRef  os.FileInfo
	childRef            os.FileInfo
	freeze, events      *os.File
	freezeRef, eventRef os.FileInfo
	parentPath, name    string
	launcher            *os.File
	launcherProc        *os.Root
	created             bool
	removed             bool
}

func prepareProcessFreezerV1(ctx context.Context) (result *processFreezerV1, returned error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	self, err := readProcessCgroupV1()
	if err != nil {
		return nil, err
	}
	native := &processCgroupV1{parentPath: self, name: "guest-" + rand.Text()}
	complete := false
	defer func() {
		if !complete {
			// Before clone3 there is no child. Cleanup still verifies that the
			// exact newly created cgroup is empty and has not been replaced.
			if native.created {
				freezer := &processFreezerV1{control: native}
				if err := freezer.closeEmpty(); err != nil {
					result = freezer // Retain this exact uncertain creation.
					returned = errors.Join(returned, err)
				}
			} else {
				returned = errors.Join(returned, native.closeDescriptors())
			}
		}
	}()
	native.root, err = openCgroupDirectoryV1(unix.AT_FDCWD, "/sys/fs/cgroup", false)
	if err != nil {
		return nil, fmt.Errorf("open the supervisor's cgroup2 mount: %w", err)
	}
	native.rootRef, err = native.root.Stat()
	if err != nil {
		return nil, err
	}
	native.parent, err = openCgroupDirectoryV1(int(native.root.Fd()), strings.TrimPrefix(self, "/"), true)
	if err != nil {
		return nil, fmt.Errorf("pin the supervisor's own cgroup: %w", err)
	}
	native.parentRef, err = native.parent.Stat()
	if err != nil {
		return nil, err
	}
	if err := unix.Mkdirat(int(native.parent.Fd()), native.name, 0700); err != nil {
		return nil, fmt.Errorf("the supervisor's own cgroup is not delegated for a fresh guest cgroup: %w", err)
	}
	native.created = true
	native.child, err = openCgroupDirectoryV1(int(native.parent.Fd()), native.name, true)
	if err != nil {
		// The exact creator's name is retained for diagnosis. A failed open
		// does not authorise unlinking an object that may have replaced it.
		return nil, fmt.Errorf("pin the newly created guest cgroup: %w", err)
	}
	native.childRef, err = native.child.Stat()
	if err != nil || os.SameFile(native.parentRef, native.childRef) {
		return nil, errors.Join(fmt.Errorf("the guest cgroup has no distinct native creation"), err)
	}
	native.events, native.eventRef, err = native.openControl("cgroup.events", os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	native.freeze, native.freezeRef, err = native.openControl("cgroup.freeze", os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	if err := native.check(false); err != nil {
		return nil, err
	}
	frozen, populated, err := native.state()
	if err != nil || frozen || populated {
		return nil, errors.Join(fmt.Errorf("the fresh guest cgroup is not empty and thawed"), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	complete = true
	return &processFreezerV1{control: native}, nil
}

func openCgroupDirectoryV1(parent int, name string, beneath bool) (*os.File, error) {
	if name == "" {
		name = "."
	}
	resolve := uint64(unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS)
	if beneath {
		resolve |= unix.RESOLVE_BENEATH
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: resolve})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "owned-cgroup")
	var facts unix.Statfs_t
	if err := unix.Fstatfs(fd, &facts); err != nil || facts.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errors.Join(fmt.Errorf("the owned cgroup is not on cgroup2"), err, file.Close())
	}
	return file, nil
}

func readProcessCgroupV1() (string, error) {
	file, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, cgroupReadBoundV1+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return "", err
	}
	return decodeProcessCgroupV1(data)
}

func decodeProcessCgroupV1(data []byte) (string, error) {
	if len(data) > cgroupReadBoundV1 {
		return "", fmt.Errorf("the native cgroup membership exceeds its kernel path bound")
	}
	var own string
	for line := range strings.SplitSeq(string(data), "\n") {
		value, unified := strings.CutPrefix(line, "0::")
		if !unified {
			continue
		}
		if own != "" || !path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("the native unified cgroup membership is ambiguous or invalid")
		}
		own = value
	}
	if own == "" {
		return "", fmt.Errorf("the supervisor has no unified cgroup membership")
	}
	return own, nil
}

func (n *processCgroupV1) openControl(name string, flags int) (*os.File, os.FileInfo, error) {
	fd, err := unix.Openat2(int(n.child.Fd()), name, &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, nil, fmt.Errorf("open the original %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), "owned-cgroup-control")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.Join(fmt.Errorf("the cgroup control is not a native regular file"), err, file.Close())
	}
	return file, info, nil
}

// bindCreatedLauncherV1 is called only with cmd.Start's actual PidFD result.
// Neither the guest's init descriptor nor a recovered process number can bind
// this cgroup to a new or replacement creation.
func (n *processCgroupV1) bindCreatedLauncherV1(fd, pid int) error {
	if n == nil || n.launcher != nil || fd < 0 || pid <= 0 {
		return fmt.Errorf("the process freezer has no original launcher creation")
	}
	n.launcher = os.NewFile(uintptr(fd), "original-freezer-launcher")
	if err := n.checkLauncherLiveV1(); err != nil {
		return err
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return err
	}
	n.launcherProc = root
	return n.check(true)
}

func (n *processCgroupV1) checkLauncherLiveV1() error {
	if n.launcher == nil {
		return fmt.Errorf("the freezer has no original launcher pidfd")
	}
	if err := unix.PidfdSendSignal(int(n.launcher.Fd()), 0, nil, 0); err != nil {
		return fmt.Errorf("check the original launcher pidfd: %w", err)
	}
	poll := []unix.PollFd{{Fd: int32(n.launcher.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(poll, 0)
	if err != nil || count != 0 {
		return errors.Join(fmt.Errorf("the original freezer launcher has ended"), err)
	}
	return nil
}

func (n *processCgroupV1) check(live bool) error {
	if n == nil || n.root == nil || n.parent == nil || n.child == nil || n.rootRef == nil || n.parentRef == nil || n.childRef == nil || n.removed {
		return fmt.Errorf("the original guest cgroup descriptors are unavailable")
	}
	currentRoot, err := openCgroupDirectoryV1(unix.AT_FDCWD, "/sys/fs/cgroup", false)
	if err != nil {
		return err
	}
	info, statErr := currentRoot.Stat()
	closeErr := currentRoot.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(n.rootRef, info) {
		return errors.Join(fmt.Errorf("the original cgroup2 mount changed"), statErr, closeErr)
	}
	currentParent, err := openCgroupDirectoryV1(int(n.root.Fd()), strings.TrimPrefix(n.parentPath, "/"), true)
	if err != nil {
		return err
	}
	info, statErr = currentParent.Stat()
	closeErr = currentParent.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(n.parentRef, info) {
		return errors.Join(fmt.Errorf("the supervisor's original cgroup parent changed"), statErr, closeErr)
	}
	currentChild, err := openCgroupDirectoryV1(int(n.parent.Fd()), n.name, true)
	if err != nil {
		return err
	}
	info, statErr = currentChild.Stat()
	closeErr = currentChild.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(n.childRef, info) {
		return errors.Join(fmt.Errorf("the original guest cgroup creation changed"), statErr, closeErr)
	}
	if !live {
		return nil
	}
	self, err := readProcessCgroupV1()
	if err != nil || self != n.parentPath {
		return errors.Join(fmt.Errorf("the original freezer supervisor left its own cgroup"), err)
	}
	if err := n.checkLauncherLiveV1(); err != nil {
		return err
	}
	if n.launcherProc == nil {
		return fmt.Errorf("the original launcher has no pinned native membership")
	}
	file, err := n.launcherProc.Open("cgroup")
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, cgroupReadBoundV1+1))
	closeErr = file.Close()
	member, decodeErr := decodeProcessCgroupV1(data)
	if err := errors.Join(readErr, closeErr, decodeErr); err != nil || member != path.Join(n.parentPath, n.name) {
		return errors.Join(fmt.Errorf("the original launcher is outside its created guest cgroup"), err)
	}
	return n.checkLauncherLiveV1()
}

func (n *processCgroupV1) write(freeze bool) error {
	if err := n.check(false); err != nil {
		return err
	}
	if n.freeze == nil || n.freezeRef == nil {
		return fmt.Errorf("the original cgroup freezer descriptor is unavailable")
	}
	file, info, err := n.openControl("cgroup.freeze", os.O_WRONLY)
	if err != nil {
		return err
	}
	closeErr := file.Close()
	if closeErr != nil || !os.SameFile(n.freezeRef, info) {
		return errors.Join(fmt.Errorf("the original cgroup freezer control changed"), closeErr)
	}
	value := byte('0')
	if freeze {
		value = '1'
	}
	written, err := n.freeze.WriteAt([]byte{value}, 0)
	if err == nil && written != 1 {
		err = io.ErrShortWrite
	}
	return err
}

func (n *processCgroupV1) state() (bool, bool, error) {
	if n == nil || n.events == nil || n.eventRef == nil {
		return false, false, fmt.Errorf("the original cgroup events descriptor is unavailable")
	}
	file, info, err := n.openControl("cgroup.events", os.O_RDONLY)
	if err != nil {
		return false, false, err
	}
	closeErr := file.Close()
	if closeErr != nil || !os.SameFile(n.eventRef, info) {
		return false, false, errors.Join(fmt.Errorf("the original cgroup events control changed"), closeErr)
	}
	var buffer [cgroupReadBoundV1 + 1]byte
	count, err := n.events.ReadAt(buffer[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, false, err
	}
	return decodeCgroupEventsV1(buffer[:count])
}

func decodeCgroupEventsV1(data []byte) (bool, bool, error) {
	if len(data) > cgroupReadBoundV1 {
		return false, false, fmt.Errorf("the native cgroup events exceed their bound")
	}
	values := make(map[string]bool, 2)
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return false, false, fmt.Errorf("the native cgroup event has invalid fields")
		}
		if fields[0] != "frozen" && fields[0] != "populated" {
			continue // The kernel may add other counters.
		}
		if _, duplicate := values[fields[0]]; duplicate || fields[1] != "0" && fields[1] != "1" {
			return false, false, fmt.Errorf("the native cgroup event is duplicated or invalid")
		}
		values[fields[0]] = fields[1] == "1"
	}
	frozen, hasFrozen := values["frozen"]
	populated, hasPopulated := values["populated"]
	if !hasFrozen || !hasPopulated {
		return false, false, fmt.Errorf("the native cgroup did not report both freeze and population state")
	}
	return frozen, populated, nil
}

func (n *processCgroupV1) release() (bool, error) {
	if err := n.check(false); err != nil {
		return false, err
	}
	claimed := "guest-cleanup-" + rand.Text()
	if err := unix.Renameat2(int(n.parent.Fd()), n.name, int(n.parent.Fd()), claimed, unix.RENAME_NOREPLACE); err != nil {
		return false, fmt.Errorf("claim the original empty guest cgroup: %w", err)
	}
	current, err := openCgroupDirectoryV1(int(n.parent.Fd()), claimed, true)
	if err != nil {
		return false, err
	}
	info, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(n.childRef, info) {
		restoreErr := unix.Renameat2(int(n.parent.Fd()), claimed, int(n.parent.Fd()), n.name, unix.RENAME_NOREPLACE)
		return false, errors.Join(fmt.Errorf("the cgroup changed before owned removal"), statErr, closeErr, restoreErr)
	}
	n.name = claimed // A failed removal retains this SAME claimed creation.
	if err := unix.Unlinkat(int(n.parent.Fd()), n.name, unix.AT_REMOVEDIR); err != nil {
		return false, fmt.Errorf("remove the original empty guest cgroup: %w", err)
	}
	n.removed = true
	return true, n.closeDescriptors()
}

func (n *processCgroupV1) closeDescriptors() error {
	var result error
	if n.launcherProc != nil {
		result = errors.Join(result, n.launcherProc.Close())
		n.launcherProc = nil
	}
	for _, file := range []*os.File{n.launcher, n.events, n.freeze, n.child, n.parent, n.root} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	n.launcher, n.events, n.freeze, n.child, n.parent, n.root = nil, nil, nil, nil, nil, nil
	return result
}
