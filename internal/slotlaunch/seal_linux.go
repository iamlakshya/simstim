//go:build linux

package slotlaunch

import (
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const vmmDataSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL

// The store is immutable already. This copy serves the distinct unreadable
// external-exec invariant that keeps the unmodified VMM non-dumpable.
func sealSlotVMM(source *os.File, digest string) (_ *os.File, returned error) {
	policy, err := os.ReadFile("/proc/sys/fs/suid_dumpable")
	if err != nil || strings.TrimSpace(string(policy)) != "0" {
		return nil, refused("the root slot needs fs.suid_dumpable=0 for its execute-only VMM")
	}
	st, err := source.Stat()
	if err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("slot-vmm", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_EXEC)
	if errors.Is(err, unix.EINVAL) {
		fd, err = unix.MemfdCreate("slot-vmm", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-execute-only-vmm")
	defer func() {
		if returned != nil {
			file.Close()
		}
	}()
	if _, err := source.Seek(0, 0); err != nil {
		return nil, err
	}
	if err := checkPayload(file, source, st.Size(), digest); err != nil {
		return nil, err
	}
	if err := file.Chmod(0o111); err != nil {
		return nil, err
	}
	_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, vmmDataSeals|unix.F_SEAL_EXEC)
	if errors.Is(err, unix.EINVAL) {
		_, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, vmmDataSeals)
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	return file, nil
}

func checkSlotVMM(file *os.File, digest string) error {
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o111 {
		return refused("the actual VMM is not its execute-only immutable copy")
	}
	var native unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(file.Fd()), &native) != nil || native.Nlink != 0 || unix.Fstatfs(int(file.Fd()), &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
		return refused("the actual VMM copy has no original unlinked memfd identity")
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&vmmDataSeals != vmmDataSeals {
		return refused("the actual VMM copy lacks its immutable data seals")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := checkPayload(io.Discard, file, st.Size(), digest); err != nil {
		return err
	}
	_, err = file.Seek(0, 0)
	return err
}
