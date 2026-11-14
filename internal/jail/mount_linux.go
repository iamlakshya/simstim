package jail

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// setupDir is where init mounts its working tmpfs. Every Linux host has
// it, and the mount is in the jail's own mount namespace, so the host's
// /tmp stays as it is.
const setupDir = "/tmp"

// dirMode is the mode of every folder that init makes on the jail's
// tmpfs: a subuid must pass through it, whatever the umask.
const dirMode = 0o755

// buildRoot gives this mount namespace a new root that holds only the
// binds and a new proc. It runs in the jail's user namespace, as init,
// with CAP_SYS_ADMIN there. fds holds each bind, opened in this namespace.
// Each of execs is an Exec rule beneath a writable bind, which gets a mount
// of its own that may run files, opened as execFDs holds. With sub, init
// gives each writable folder that the login user owns to the subuid.
//
// It mounts a tmpfs on setupDir and makes it the root, with the host's root
// at /oldroot. It builds the jail's root on a second tmpfs at /newroot,
// from /oldroot, then makes that the root and drops the rest. Nothing
// reaches the host: every mount is private first.
func buildRoot(binds []bind, fds []int, execs []execMount, execFDs []int, sub bool) error {
	old := unix.Umask(0)
	defer unix.Umask(old)
	root := -1
	defer func() {
		if root >= 0 {
			_ = unix.Close(root)
		}
	}()
	steps := []func() error{
		func() error { return mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "") },
		func() error { return mount("tmpfs", setupDir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755") },
		func() error { return chdir(setupDir) },
		func() error { return mkdir("newroot") },
		func() error { return mount("tmpfs", "newroot", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755") },
		func() error {
			var err error
			root, err = openat2(unix.AT_FDCWD, "newroot", &unix.OpenHow{
				Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
			})
			if err != nil {
				return stepFailed("cannot open the jail's empty root", "Check that the host supports openat2 and private mounts.", err)
			}
			return nil
		},
		func() error { return mkdir("oldroot") },
		func() error { return pivotRoot(".", "oldroot") },
		func() error { return chdir("/") },
	}
	if err := runMountSteps(steps); err != nil {
		return err
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(root, &rootStat); err != nil {
		return stepFailed("cannot read the jail's tmpfs", "Report this as a bug.", err)
	}
	var err error
	plan := newMountGraph(binds, fds, execs, execFDs)
	root, rootStat.Dev, err = mountPlanned(root, rootStat.Dev, plan, sub, resetMountRoot)
	if err != nil {
		return err
	}
	proc, err := resolveTarget(root, rootStat.Dev, "/proc", true, true)
	if err != nil {
		return err
	}
	err = mount("proc", mountFDPath(proc.dstFD), "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "")
	proc.close()
	if err != nil {
		return err
	}
	finalTargets, err := plan.targets(root, rootStat.Dev, nil)
	if err != nil {
		return err
	}
	if err := plan.verify(root, rootStat.Dev, finalTargets); err != nil {
		return err
	}
	return runMountSteps([]func() error{
		func() error { return unmount("/oldroot") },
		func() error {
			if err := unix.Fchdir(root); err != nil {
				return stepFailed("cannot enter the jail's root", "Report this as a bug.", err)
			}
			_ = unix.Close(root)
			root = -1
			return nil
		},
		func() error { return pivotRoot(".", ".") },
		func() error { return unmount(".") },
		func() error { return chdir("/") },
		func() error {
			return mount("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "")
		},
	})
}

func runMountSteps(steps []func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// bindInto shows the object that fd holds at b.Path in the new root,
// read-only unless b.Write says otherwise. It keeps the flags that the
// source mount locks in a user namespace, and adds nosuid, nodev unless b
// is a device, and noexec on a writable bind.
func bindInto(root int, dev uint64, b bind, fd int, sub bool) error {
	dst, err := resolveTarget(root, dev, b.Path, b.Kind == kindDir, true)
	if err != nil {
		return err
	}
	defer dst.close()
	if sub && b.Write {
		// Inside, the login user is 0 and the subuid is 1. Only a folder or
		// file of the login user's own changes owner.
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err == nil && st.Uid == 0 && b.Kind != kindDevice {
			if err := unix.Fchownat(fd, "", 1, -1, unix.AT_EMPTY_PATH); err != nil {
				return stepFailed("cannot give "+quote(b.Path)+" to the subuid", "Report this as a bug.", err)
			}
		}
	}
	var flags uintptr
	if b.Write {
		// Landlock's Exec right covers execve only. noexec also stops the
		// loader, which maps a file to run it.
		flags |= unix.MS_NOEXEC
	} else {
		flags |= unix.MS_RDONLY
	}
	if b.Kind != kindDevice {
		flags |= unix.MS_NODEV
	}
	return bindFD(fd, dst, b.Path, flags)
}

// execInto shows an Exec rule's path, beneath a writable bind, on a mount
// of its own that may run files. It is read-only unless the rule also has
// Write, and the rest of the bind stays noexec.
func execInto(root int, dev uint64, r execMount, fd int) error {
	dst, err := resolveTarget(root, dev, r.Path, r.Dir, false)
	if err != nil {
		return err
	}
	defer dst.close()
	var srcStat, dstStat unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &srcStat), unix.Fstat(dst.dstFD, &dstStat)); err != nil {
		return stepFailed("cannot read the executable mount", "Keep bound paths unchanged during start.", err)
	}
	if srcStat.Dev != dstStat.Dev || srcStat.Ino != dstStat.Ino {
		return &targetFailure{detail: failure("the executable mount target changed", "Keep bound paths unchanged during start."),
			cause: errExecutableTargetChanged}
	}
	flags := uintptr(unix.MS_NODEV)
	if r.Access&Write == 0 {
		flags |= unix.MS_RDONLY
	}
	if r.noexec {
		flags |= unix.MS_NOEXEC
	}
	return bindFD(fd, dst, r.Path, flags)
}

// bindFD mounts the object that fd holds on dst, then mounts it again with
// flags and nosuid. It keeps the flags that the source mount locks in a
// user namespace.
func bindFD(fd int, dst resolvedTarget, name string, flags uintptr) error {
	if err := mount(mountFDPath(fd), mountFDPath(dst.dstFD), "", unix.MS_BIND, ""); err != nil {
		return err
	}
	mounted, err := openPath(dst.parentFD, dst.base, unix.RESOLVE_BENEATH|unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		return pinError("cannot reopen the mounted target "+quote(name), "Keep bound paths unchanged during start.", err)
	}
	defer func() { _ = unix.Close(mounted) }()
	var source, target unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &source), unix.Fstat(mounted, &target)); err != nil {
		return stepFailed("cannot check the mounted target "+quote(name), "Keep bound paths unchanged during start.", err)
	}
	if source.Dev != target.Dev || source.Ino != target.Ino {
		return failure("the mounted target "+quote(name)+" changed", "Keep bound paths unchanged during start.")
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(mounted, &st); err != nil {
		return stepFailed("cannot read the mount at "+quote(name), "Report this as a bug.", err)
	}
	flags |= unix.MS_BIND | unix.MS_REMOUNT | unix.MS_NOSUID
	for _, f := range [][2]uintptr{{unix.ST_RDONLY, unix.MS_RDONLY}, {unix.ST_NODEV, unix.MS_NODEV}, {unix.ST_NOEXEC, unix.MS_NOEXEC}} {
		if uintptr(st.Flags)&f[0] != 0 { //nolint:gosec // G115: mount flags are small and positive
			flags |= f[1]
		}
	}
	return mount("", mountFDPath(mounted), "", flags, "")
}

func mount(src, dst, fstype string, flags uintptr, data string) error {
	if err := unix.Mount(src, dst, fstype, flags, data); err != nil {
		what := src
		if what == "" {
			what = fstype
		}
		return stepFailed("cannot mount "+quote(what)+" on "+quote(dst)+" in the jail",
			"Check that each path in Spec.Binds exists, and that this host gives user namespaces.", err)
	}
	return nil
}

func unmount(path string) error {
	if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
		return stepFailed("cannot unmount "+quote(path)+" in the jail", "Report this as a bug.", err)
	}
	return nil
}

// errRootfs marks pivot_root's EINVAL on a rootfs root, which pivot_root
// cannot leave: a clear no, where every other EINVAL is not.
var errRootfs = errors.New("the root is a rootfs")

// Test hooks: pivot_root, and whether the root is a rootfs.
var (
	pivotRootFn = unix.PivotRoot
	rootfsFn    = onRootfs
)

func pivotRoot(newRoot, putOld string) error {
	err := pivotRootFn(newRoot, putOld)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EINVAL) && rootfsFn() {
		err = errors.Join(errRootfs, err)
	}
	return stepFailed("cannot change the jail's root", "Check that the host's root is not a rootfs, which pivot_root cannot leave.", err)
}

// onRootfs says whether this process's root is the rootfs.
func onRootfs() bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	return rootfsIn(string(b))
}

// rootfsIn says whether the last mount on "/" in a mountinfo text is a
// rootfs. Each line holds the mount point as its fifth field, and the type
// after " - ".
func rootfsIn(mountinfo string) bool {
	fstype := ""
	for line := range strings.SplitSeq(mountinfo, "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		f := strings.Fields(pre)
		if ok && len(f) >= 5 && f[4] == "/" {
			fstype, _, _ = strings.Cut(post, " ")
		}
	}
	return fstype == "rootfs"
}

func chdir(path string) error {
	if err := unix.Chdir(path); err != nil {
		return stepFailed("cannot enter "+quote(path)+" in the jail", "Report this as a bug.", err)
	}
	return nil
}

// mkdir makes a folder with dirMode, whatever the umask, or leaves one that
// is there.
func mkdir(path string) error {
	err := unix.Mkdir(path, dirMode)
	if errors.Is(err, unix.EEXIST) {
		return nil
	}
	if err == nil {
		err = unix.Chmod(path, dirMode)
	}
	if err != nil {
		return stepFailed("cannot make "+quote(path)+" in the jail", "Report this as a bug.", err)
	}
	return nil
}
