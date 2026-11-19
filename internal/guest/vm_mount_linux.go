//go:build linux

package guest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type vmMountOps struct {
	mount   func(string, string, string, uintptr, string) error
	unmount func(string, int) error
	pivot   func(string, string) error
	chdir   func(string) error
	own     func(*os.File, int, int) error
	node    func(string, uint32, int) error
	chmod   func(string, uint32) error
}

func realVMMounts() vmMountOps {
	return vmMountOps{mount: unix.Mount, unmount: unix.Unmount, pivot: unix.PivotRoot, chdir: unix.Chdir, own: func(file *os.File, uid, gid int) error { return file.Chown(uid, gid) }, node: unix.Mknod, chmod: unix.Chmod}
}

func (v *vmFiles) ownedDirectory(root *os.Root, name string, mode os.FileMode, uid, gid int) error {
	return vmOwnedDirectory(root, name, mode, uid, gid, v.ops.own)
}

type vmMounted struct{ target string }

type vmFiles struct {
	imageSaveRoot, imageSaveUpper *os.Root
	copyNativeOwner               *vmInitConnection
	copyMounts                    map[string]*vmCopyMountV1
	copyStageRoots                []vmCopyStageRootV1
	base                          string
	root                          string
	ops                           vmMountOps
	mounts                        []vmMounted
	pivoted                       bool
	back                          bool
}

func newVMFiles(base string, ops vmMountOps) *vmFiles {
	return &vmFiles{base: base, root: filepath.Join(base, "mnt/root"), ops: ops}
}

func (v *vmFiles) boot(name string) string { return filepath.Join(v.base, name) }

func (v *vmFiles) mount(source, target, kind string, flags uintptr, data string) error {
	if err := v.ops.mount(source, target, kind, flags, data); err != nil {
		return fmt.Errorf("mount guest%s: %w", target, err)
	}
	v.mounts = append(v.mounts, vmMounted{target: target})
	return nil
}

// mountLive records a post-pivot mount with the same pre-pivot target keys as
// layout. Teardown can then unmount cgroup2 BEFORE its parent sysfs mount.
func (v *vmFiles) mountLive(source, target, kind string, flags uintptr, data string) error {
	if !v.pivoted || v.back || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return vmError("invalid live guest mount phase or path")
	}
	if err := v.ops.mount(source, target, kind, flags, data); err != nil {
		return err
	}
	v.mounts = append(v.mounts, vmMounted{target: filepath.Join(v.root, strings.TrimPrefix(target, "/"))})
	return nil
}

func (v *vmFiles) bind(source, target string, readonly, nodev bool) error {
	if err := v.mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_NOSUID)
	if readonly {
		flags |= unix.MS_RDONLY
	}
	if nodev {
		flags |= unix.MS_NODEV
	}
	if err := v.ops.mount("", target, "", flags, ""); err != nil {
		return fmt.Errorf("limit guest bind%s: %w", target, err)
	}
	return nil
}

func (v *vmFiles) bootstrap() error {
	for _, m := range []struct {
		source, target, kind string
		flags                uintptr
		data                 string
	}{
		{"devtmpfs", "dev", "devtmpfs", unix.MS_NOSUID, ""},
		{"proc", "proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"tmpfs", "run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
	} {
		if err := v.mount(m.source, v.boot(m.target), m.kind, m.flags, m.data); err != nil {
			return err
		}
	}
	return nil
}

func (v *vmFiles) mountDisks() error {
	for _, m := range []struct {
		device, target string
		readonly       bool
	}{
		{"dm-1", "mnt/image", true}, {"vdc", "mnt/agents", true}, {"vdd", "mnt/session", false},
	} {
		flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
		if m.readonly {
			flags |= unix.MS_RDONLY
		}
		if err := v.mount(v.boot("dev/"+m.device), v.boot(m.target), "ext4", flags, ""); err != nil {
			return err
		}
	}
	return nil
}

func (v *vmFiles) overlay(system string, sudo bool) error {
	upperBase := v.boot("run/overlay")
	if system == "keep" {
		upperBase = v.boot("mnt/session/system")
	} else if system != "reset" {
		return fmt.Errorf("unknown resolved VM system mode")
	}
	parent := filepath.Dir(upperBase)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	if err := vmDirectory(root, filepath.Base(upperBase), 0o700); err != nil {
		root.Close()
		return err
	}
	root.Close()
	if system == "reset" {
		if err := v.mount("tmpfs", upperBase, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
			return err
		}
	}
	root, err = os.OpenRoot(upperBase)
	if err != nil {
		return err
	}
	for _, name := range []string{"upper", "work"} {
		if err := vmDirectory(root, name, 0o700); err != nil {
			root.Close()
			return err
		}
	}
	root.Close()
	flags := uintptr(unix.MS_NODEV)
	if !sudo {
		flags |= unix.MS_NOSUID
	}
	data := "lowerdir=" + v.boot("mnt/image") + ",upperdir=" + filepath.Join(upperBase, "upper") + ",workdir=" + filepath.Join(upperBase, "work")
	if err := v.mount("overlay", v.root, "overlay", flags, data); err != nil {
		return err
	}
	return v.captureImageSaveRootsV1(upperBase)
}

func (v *vmFiles) pivotInto() error {
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return err
	}
	if err := vmDirectory(root, ".oldroot", 0o700); err != nil {
		root.Close()
		return err
	}
	root.Close()
	if err := v.ops.pivot(v.root, filepath.Join(v.root, ".oldroot")); err != nil {
		return fmt.Errorf("switch guest root: %w", err)
	}
	if err := v.ops.chdir("/"); err != nil {
		return err
	}
	v.pivoted = true
	// The separate read-only .tools bind retains only the EROFS root, not its
	// old dev/proc/session child mounts. No image process starts before detach.
	if err := v.ops.unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach guest boot root: %w", err)
	}
	return os.Remove("/.oldroot")
}

// pivotBack releases a persistent overlay upper before unmounting its disk.
// The EROFS tools root already has the empty mnt/root target from P31.
func (v *vmFiles) pivotBack() error {
	if !v.pivoted || v.back {
		return nil
	}
	if err := v.ops.pivot("/.tools", "/.tools/mnt/root"); err != nil {
		return fmt.Errorf("return to the guest tools root: %w", err)
	}
	if err := v.ops.chdir("/"); err != nil {
		return err
	}
	v.back = true
	return nil
}

func (v *vmFiles) close() error {
	result := errors.Join(v.closeImageSaveRootsV1(), v.closeCopyMountsV1())
	for i := len(v.mounts) - 1; i >= 0; i-- {
		target := v.mounts[i].target
		if v.pivoted {
			if !v.back {
				continue
			}
			if target == filepath.Join(v.root, ".tools") {
				continue
			}
			if relative, err := filepath.Rel(v.root, target); err == nil && relative != ".." && !strings.HasPrefix(relative, "../") && !filepath.IsAbs(relative) {
				target = filepath.Join("/mnt/root", relative)
			} else {
				continue
			}
		}
		if err := v.ops.unmount(target, 0); err != nil {
			result = errors.Join(result, fmt.Errorf("unmount guest%s: %w", target, err))
		}
	}
	return result
}
