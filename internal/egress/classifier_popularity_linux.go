//go:build linux

package egress

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const popularitySealsV1 = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL

func newPopularitySnapshotV1() (*os.File, error) {
	flags := unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING
	fd, err := unix.MemfdCreate("classifier-popularity", flags|unix.MFD_NOEXEC_SEAL)
	if err == unix.EINVAL {
		fd, err = unix.MemfdCreate("classifier-popularity", flags)
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "classifier-popularity")
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func sealPopularitySnapshotV1(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, popularityErrorV1()
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, popularitySealsV1); err != nil {
		return nil, popularityErrorV1()
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(int(file.Fd())), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, popularityErrorV1()
	}
	sealed := os.NewFile(uintptr(fd), "classifier-popularity")
	before, err := popularityFileVersionV1Of(file)
	if err != nil {
		_ = sealed.Close()
		return nil, popularityErrorV1()
	}
	after, err := popularitySnapshotVersionV1(sealed)
	if err != nil || before != after {
		_ = sealed.Close()
		return nil, popularityErrorV1()
	}
	return sealed, nil
}

func popularityFileVersionV1Of(file *os.File) (popularityFileVersionV1, error) {
	var stat unix.Stat_t
	if file == nil || unix.Fstat(int(file.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return popularityFileVersionV1{}, popularityErrorV1()
	}
	return popularityFileVersionV1{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: stat.Size, Modified: int64(stat.Mtim.Sec)*1e9 + int64(stat.Mtim.Nsec), Changed: int64(stat.Ctim.Sec)*1e9 + int64(stat.Ctim.Nsec), Mode: stat.Mode}, nil
}

func popularityOriginalVersionV1(file *os.File) (popularityFileVersionV1, error) {
	version, err := popularityFileVersionV1Of(file)
	if err != nil {
		return version, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return popularityFileVersionV1{}, popularityErrorV1()
	}
	return version, nil
}

func popularitySnapshotVersionV1(file *os.File) (popularityFileVersionV1, error) {
	version, err := popularityFileVersionV1Of(file)
	if err != nil || version.Mode&0o111 != 0 {
		return popularityFileVersionV1{}, popularityErrorV1()
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return popularityFileVersionV1{}, popularityErrorV1()
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&popularitySealsV1 != popularitySealsV1 {
		return popularityFileVersionV1{}, popularityErrorV1()
	}
	return version, nil
}

func popularityCloseOnExecV1(file *os.File) { unix.CloseOnExec(int(file.Fd())) }
