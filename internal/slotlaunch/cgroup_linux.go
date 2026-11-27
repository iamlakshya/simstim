//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// cgroupIdentity is minted only from a root-created native cgroup. A saved
// PID or a status claimed by the slot is never a vacancy or reuse witness.
type cgroupIdentity struct {
	Path      string `json:"path"`
	Dev       uint64 `json:"dev"`
	Ino       uint64 `json:"ino"`
	ParentDev uint64 `json:"parent_dev"`
	ParentIno uint64 `json:"parent_ino"`
}

var bootIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func nativeBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	value := strings.TrimSpace(string(data))
	if err != nil || !bootIDPattern.MatchString(value) {
		return "", refused("the native host boot identity is unavailable")
	}
	return value, nil
}

func cgroupRoot() (*os.Root, error) {
	root, err := openOwnedRoot("/sys/fs/cgroup", 0)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer file.Close()
	var stat unix.Statfs_t
	if unix.Fstatfs(int(file.Fd()), &stat) != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
		root.Close()
		return nil, refused("the slot fence needs the native unified cgroup mount")
	}
	return root, nil
}

func captureCurrentCgroup() (*os.File, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	name, err := unifiedPath(string(raw))
	if err != nil {
		return nil, err
	}
	root, err := cgroupRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b := &backend{root: root, owner: 0}
	relative := strings.TrimPrefix(name, "/")
	if relative == "" {
		relative = "."
	}
	return b.open(relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
}

func sameNativeCgroupParent(child, parent *os.File) error {
	if child == nil || parent == nil {
		return refused("the native root source has no original caller cgroup")
	}
	fd, err := unix.Openat(int(child.Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	actual := os.NewFile(uintptr(fd), "original-cgroup-parent")
	defer actual.Close()
	left, err := actual.Stat()
	if err != nil {
		return err
	}
	right, err := parent.Stat()
	if err != nil || !os.SameFile(left, right) {
		return refused("sudo moved the VMM outside the original caller's session cgroup")
	}
	return nil
}

func unifiedPath(raw string) (string, error) {
	var found string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		if found != "" {
			return "", refused("the current process has ambiguous unified cgroup membership")
		}
		found = strings.TrimPrefix(line, "0::")
	}
	if found == "" || path.Clean(found) != found || strings.ContainsAny(found, "\x00\r\n") {
		return "", refused("the current process has no canonical unified cgroup membership")
	}
	return found, nil
}

func openCgroup(root *os.Root, identity cgroupIdentity) (*os.File, error) {
	if !strings.HasPrefix(identity.Path, "/") || path.Clean(identity.Path) != identity.Path || identity.Ino == 0 {
		return nil, refused("the protected slot cgroup identity is invalid")
	}
	b := &backend{root: root, owner: 0}
	file, err := b.open(strings.TrimPrefix(identity.Path, "/"), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	st, err := file.Stat()
	if err != nil || !owned(st, 0) || st.Mode().Perm()&0o022 != 0 {
		file.Close()
		return nil, refused("the slot cgroup is no longer a nondelegated root object")
	}
	stat := st.Sys().(*syscall.Stat_t)
	if uint64(stat.Dev) != identity.Dev || stat.Ino != identity.Ino {
		file.Close()
		return nil, refused("the slot cgroup's native identity changed")
	}
	if err := checkCgroupParent(file, identity); err != nil {
		file.Close()
		return nil, err
	}
	for _, name := range []string{"cgroup.procs", "cgroup.threads"} {
		var control unix.Stat_t
		err := unix.Fstatat(int(file.Fd()), name, &control, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil || control.Uid != 0 || control.Mode&0o077 != 0 || control.Mode&unix.S_IFMT != unix.S_IFREG {
			file.Close()
			return nil, refused("the slot cgroup migration controls are delegated")
		}
	}
	return file, nil
}

func populated(file *os.File) (bool, error) {
	fd, err := unix.Openat(int(file.Fd()), "cgroup.events", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return true, err
	}
	events := os.NewFile(uintptr(fd), "cgroup.events")
	defer events.Close()
	raw, err := io.ReadAll(io.LimitReader(events, 4097))
	if err != nil || len(raw) > 4096 {
		return true, refused("the slot cgroup population cannot be read")
	}
	count := 0
	live := true
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "populated ") {
			count++
			switch line {
			case "populated 0":
				live = false
			case "populated 1":
				live = true
			default:
				return true, refused("the slot cgroup population is malformed")
			}
		}
	}
	if count != 1 {
		return true, refused("the slot cgroup has no unique population witness")
	}
	return live, nil
}

func killNativeCgroup(ctx context.Context, file *os.File) error {
	fd, err := unix.Openat(int(file.Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	n, err := unix.Write(fd, []byte("1"))
	_ = unix.Close(fd)
	if err != nil || n != 1 {
		return refused("the original root cgroup could not be stopped")
	}
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		live, err := populated(file)
		if err != nil {
			return err
		}
		if !live {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return refused("the original root cgroup is still populated; its slot remains quarantined")
}

func (b *backend) vacancy(slot int) error {
	var record slotRecord
	err := b.readJSON(slotRecordName(slot), &record)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Slot != slot || record.Cgroup == nil {
		return refused("the previous slot allocation has no original native cgroup fence")
	}
	boot, err := nativeBootID()
	if err != nil {
		return err
	}
	if !bootIDPattern.MatchString(record.BootID) {
		return refused("the previous slot allocation has no native boot identity")
	}
	// The kernel's actual boot UUID changed. No process or cgroup from that
	// boot survives; this settles the old allocation without adopting a new
	// cgroup object at its former path.
	if boot != record.BootID {
		return nil
	}
	root, err := cgroupRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := openCgroup(root, *record.Cgroup)
	if err != nil {
		return err
	}
	defer file.Close()
	live, err := populated(file)
	if err != nil {
		return err
	}
	if live {
		return refused("the previous slot cgroup is still populated; the slot remains quarantined")
	}
	return nil
}

func makeCgroup(current string, slot int) (*os.File, cgroupIdentity, error) {
	var identity cgroupIdentity
	parent, err := unifiedPath(current)
	if err != nil {
		return nil, identity, err
	}
	root, err := cgroupRoot()
	if err != nil {
		return nil, identity, err
	}
	defer root.Close()
	b := &backend{root: root, owner: 0}
	name, err := temporary(path.Join(strings.TrimPrefix(parent, "/"), fmt.Sprintf("%s-slot-%02d", brand.Name, slot)))
	if err != nil {
		return nil, identity, err
	}
	// Each allocation gets an exclusive kernel object. Reusing an empty
	// object would let a delayed old owner act on a newer slot generation.
	if err := root.Mkdir(name, 0o755); err != nil {
		return nil, identity, err
	}
	file, err := b.open(name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, identity, err
	}
	st, err := file.Stat()
	if err != nil || !owned(st, 0) || st.Mode().Perm()&0o022 != 0 {
		file.Close()
		return nil, identity, refused("the native session slot cgroup is not root-owned")
	}
	stat := st.Sys().(*syscall.Stat_t)
	parentStat, err := cgroupParentStat(file)
	if err != nil {
		file.Close()
		return nil, identity, err
	}
	identity = cgroupIdentity{Path: "/" + name, Dev: uint64(stat.Dev), Ino: stat.Ino, ParentDev: uint64(parentStat.Dev), ParentIno: parentStat.Ino}
	for _, controlName := range []string{"cgroup.procs", "cgroup.threads"} {
		fd, err := unix.Openat(int(file.Fd()), controlName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			file.Close()
			return nil, identity, err
		}
		control := os.NewFile(uintptr(fd), controlName)
		err = control.Chown(0, 0)
		if err == nil {
			err = control.Chmod(0o600)
		}
		err = joinClose(err, control)
		if err != nil {
			file.Close()
			return nil, identity, err
		}
	}
	live, err := populated(file)
	if err != nil || live {
		file.Close()
		if err != nil {
			return nil, identity, err
		}
		return nil, identity, refused("the native slot cgroup is already populated")
	}
	return file, identity, nil
}
