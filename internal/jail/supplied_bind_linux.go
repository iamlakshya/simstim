package jail

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

// checkSuppliedBindV1 checks actual native source rights. An anonymous source
// is accepted only as an immutable readonly memfd. A caller flag or pathname
// cannot replace these observations or create a writable regular-file grant.
func checkSuppliedBindV1(fd int, write bool, real string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return false, err
	}
	kind := kindOf(st.Mode)
	if st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, failure("a supplied bind is not a regular file or directory", "Pass only the original checked filesystem source capabilities.")
	}
	if kind == kindFile {
		mode := flags & unix.O_ACCMODE
		if flags&unix.O_PATH != 0 || write && mode == unix.O_RDONLY || !write && mode == unix.O_WRONLY {
			return false, failure("a supplied regular-file bind exceeds its native descriptor rights", "Preserve the original source rights at the fixed target.")
		}
	}
	if !strings.HasSuffix(real, " (deleted)") {
		return false, nil
	}
	var filesystem unix.Statfs_t
	seals, sealErr := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	statErr := unix.Fstatfs(fd, &filesystem)
	base := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if write || kind != kindFile || st.Nlink != 0 || filesystem.Type != unix.TMPFS_MAGIC ||
		!strings.HasPrefix(real, "/memfd:") || sealErr != nil || statErr != nil || seals&base != base {
		return false, errors.Join(failure("the anonymous supplied bind is not its immutable readonly memfd", "Use the original sealed source; retain unknown or writable objects."), sealErr, statErr)
	}
	return true, nil
}
