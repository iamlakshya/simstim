package jail

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

func makeReadonlyFilesPrefixV1(root int, dev uint64) (int, error) {
	var st unix.Stat_t
	var fs unix.Statfs_t
	if err := errors.Join(unix.Fstat(root, &st), unix.Fstatfs(root, &fs)); err != nil {
		return -1, readonlyFileFailureV1(root, "inspect the fresh private root", err)
	}
	if st.Dev != dev || st.Mode&unix.S_IFMT != unix.S_IFDIR || fs.Type != unix.TMPFS_MAGIC || int64(st.Uid) != int64(unix.Geteuid()) {
		return -1, failure("the inherited-file projection root is not the owned fresh tmpfs", "Build and verify the jail's private root before projecting file grants.")
	}
	if err := unix.Mkdirat(root, readonlyFilesPrefixV1, 0o700); err != nil {
		return -1, readonlyFileFailureV1(root, "reserve its exclusive private mount prefix", err)
	}
	prefix, err := openat2(root, readonlyFilesPrefixV1, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == nil {
		err = errors.Join(unix.Fchmod(prefix, 0o700), unix.Fstat(prefix, &st))
	}
	if err == nil && (st.Dev != dev || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o7777 != 0o700 || int64(st.Uid) != int64(unix.Geteuid())) {
		err = fmt.Errorf("the new private projection prefix changed filesystem, owner or mode")
	}
	if err != nil {
		if prefix >= 0 {
			err = errors.Join(err, closeReadonlyFDV1(prefix))
		}
		err = errors.Join(err, unix.Unlinkat(root, readonlyFilesPrefixV1, unix.AT_REMOVEDIR))
		return -1, readonlyFileFailureV1(root, "verify its private mount prefix", err)
	}
	return prefix, nil
}

func mountReadonlyFileV1(prefix int, dev uint64, name string, file readonlyFileSnapshotV1) (opened int, result error) {
	opened = -1
	target, err := unix.Openat(prefix, name, unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return -1, readonlyFileFailureV1(file.rule.FD, "make its private regular mount target", err)
	}
	defer func() { result = errors.Join(result, closeReadonlyFDV1(target)) }()
	var targetStat unix.Stat_t
	if err := unix.Fstat(target, &targetStat); err != nil {
		return -1, readonlyFileFailureV1(file.rule.FD, "inspect its new mount target", err)
	}
	if targetStat.Dev != dev || targetStat.Mode&unix.S_IFMT != unix.S_IFREG || int64(targetStat.Uid) != int64(unix.Geteuid()) {
		return -1, failure("an inherited-file mount target escaped its owned tmpfs", "Keep the jail's private mount graph unchanged during setup.")
	}
	changed, err := beginReadonlyProgramReadV1(file)
	if err != nil {
		return -1, err
	}
	defer func() {
		if changed {
			result = errors.Join(result, restoreReadonlyProgramModeV1(file.rule.FD))
		}
		if result != nil && opened >= 0 {
			result = errors.Join(result, closeReadonlyFDV1(opened))
			opened = -1
		}
	}()
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV)
	if file.rule.Role == FileDataV1 {
		flags |= unix.MS_NOEXEC
	}
	dst := resolvedTarget{parentFD: prefix, base: name, dstFD: target, path: "/" + readonlyFilesPrefixV1 + "/" + name}
	if err := bindFD(file.rule.FD, dst, dst.path, flags); err != nil {
		return -1, err
	}
	openFlags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW)
	if file.rule.Access&Read == 0 {
		openFlags |= unix.O_PATH
	} else {
		retained := file.flags & (unix.O_APPEND | unix.O_NONBLOCK | unix.O_DIRECT | unix.O_NOATIME | unix.O_SYNC | unix.O_LARGEFILE)
		if retained < 0 {
			return -1, failure("invalid inherited regular-file flags", "Use the original checked Files descriptors.")
		}
		openFlags |= uint64(retained)
	}
	opened, err = openat2(prefix, name, &unix.OpenHow{Flags: openFlags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return -1, readonlyFileFailureV1(file.rule.FD, "reopen from its read-only mount", err)
	}
	if changed {
		err := restoreReadonlyProgramModeV1(file.rule.FD)
		changed = false // A failed restoration is reported; execution must abort.
		if err != nil {
			return opened, err
		}
	}
	if err := verifyReadonlyFileV1(opened, file); err != nil {
		return opened, err
	}
	if openFlags&unix.O_PATH == 0 {
		if _, err := unix.Seek(opened, file.offset, unix.SEEK_SET); err != nil {
			return opened, readonlyFileFailureV1(file.rule.FD, "restore its initial cursor on the new OFD", err)
		}
	}
	return opened, nil
}

// beginReadonlyProgramReadV1 never changes a host cache file. Only an owned,
// unlinked, natively base-sealed tmpfs memfd at exact mode 0111 qualifies.
// F_SEAL_EXEC protects execute bits, not adding read bits. Both modern and
// legacy sealed programs with a Read grant therefore need the same temporary
// read permission. Exec-only grants reopen O_PATH and need no mode change.
// Restoring 0111 before descriptor replacement preserves the unreadable ELF
// execution invariant used by the VMM's NoDump path; 0511 is never final.
func beginReadonlyProgramReadV1(file readonlyFileSnapshotV1) (bool, error) {
	if file.rule.Access&Read == 0 || file.rule.Role != FileProgramV1 || file.rule.Mode != 0o111 || file.flags&unix.O_PATH != 0 {
		return false, nil
	}
	fd := file.rule.FD
	var st unix.Stat_t
	var fs unix.Statfs_t
	if err := errors.Join(unix.Fstat(fd, &st), unix.Fstatfs(fd, &fs)); err != nil {
		return false, readonlyFileFailureV1(fd, "check the execute-only memfd", err)
	}
	if st.Dev != file.rule.Dev || st.Ino != file.rule.Ino || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != 0o111 || st.Nlink != 0 || fs.Type != unix.TMPFS_MAGIC || int64(st.Uid) != int64(unix.Geteuid()) {
		return false, failure("an execute-only program is not its owned unlinked tmpfs memfd", "Pass the original sealed executable memfd; keep host cache files unchanged.")
	}
	seals, err := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	baseSeals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if err != nil || seals&baseSeals != baseSeals {
		if err == nil {
			err = fmt.Errorf("the program lacks the full immutable-data base seals")
		}
		return false, readonlyFileFailureV1(fd, "check native executable memfd seals", err)
	}
	var link string
	for size := 4096; size <= 65536; size *= 2 {
		data := make([]byte, size)
		n, err := unix.Readlink(mountFDPath(fd), data)
		if err != nil {
			return false, readonlyFileFailureV1(fd, "read the kernel memfd name", err)
		}
		if n < len(data) {
			link = string(data[:n])
			break
		}
	}
	if !strings.HasPrefix(link, "/memfd:") || !strings.HasSuffix(link, " (deleted)") {
		return false, failure("the execute-only program has no kernel memfd identity", "Use the original sealed executable memfd.")
	}
	if err := unix.Fchmod(fd, 0o511); err != nil {
		return false, readonlyFileFailureV1(fd, "temporarily read the sealed executable in the trusted stage", err)
	}
	return true, nil
}

func restoreReadonlyProgramModeV1(fd int) error {
	if err := unix.Fchmod(fd, 0o111); err != nil {
		return readonlyFileFailureV1(fd, "restore the unreadable executable mode before any exec", err)
	}
	return nil
}

func verifyReadonlyFileV1(fd int, file readonlyFileSnapshotV1) error {
	got, err := inspectFileRuleV1(fd, file.rule.FileRuleV1)
	if err != nil {
		return err
	}
	if got.Dev != file.rule.Dev || got.Ino != file.rule.Ino {
		return failure("a read-only inherited-file mount changed inode", "Keep every source descriptor unchanged during jail setup.")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return readonlyFileFailureV1(file.rule.FD, "verify the reopened descriptor's native rights", err)
	}
	if file.rule.Access&Read == 0 && flags&unix.O_PATH == 0 || file.rule.Access&Read != 0 && (flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY) {
		return failure("an inherited-file projection changed its granted descriptor rights", "Reopen Exec-only grants as O_PATH and Read grants as read-only descriptors.")
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return readonlyFileFailureV1(file.rule.FD, "verify the reopened mount flags", err)
	}
	want := int64(unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV)
	if file.rule.Role == FileDataV1 {
		want |= unix.ST_NOEXEC
	}
	if fs.Flags&want != want {
		return failure("an inherited-file projection lacks its required read-only mount flags", "Use a host that can enforce private read-only bind mounts.")
	}
	return nil
}

func removeReadonlyTargetV1(prefix int, name string) error {
	err := unix.Unmount(mountFDPath(prefix)+"/"+name, unix.MNT_DETACH)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
		err = nil // A failed bind may have left only its new placeholder.
	}
	err = errors.Join(err, unix.Unlinkat(prefix, name, 0))
	if err != nil {
		return readonlyFileFailureV1(prefix, "remove a failed private file projection", err)
	}
	return nil
}
