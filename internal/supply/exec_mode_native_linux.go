//go:build linux

package supply

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// These handles remain in the trusted parent. In particular an O_PATH
// metadata handle is never a projected VMM, helper or lease capability.
func openModeArtifact(root *os.Root, item Item) (*os.File, error) {
	if !IsHex64(item.SHA256) {
		return nil, fmt.Errorf("mode lease has no canonical artifact digest")
	}
	file, err := root.OpenFile(item.SHA256, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("pin mode artifact inode: %w", err)
	}
	if _, err := modeArtifactInfo(root, item, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func modeArtifactInfo(root *os.Root, item Item, file *os.File) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	mode := info.Mode()
	if mode != 0o555 && mode != 0o111 {
		return nil, fmt.Errorf("mode artifact is not a canonical VMM inode")
	}
	if info.Size() != item.Size {
		return nil, &MismatchError{Name: item.Name, What: "size", Want: strconv.FormatInt(item.Size, 10), Got: strconv.FormatInt(info.Size(), 10)}
	}
	return cachefs.ValidateFile(root, item.SHA256, file, mode)
}

func modeMountWritable(file *os.File) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stat); err != nil {
		return false, err
	}
	return stat.Flags&unix.ST_RDONLY == 0, nil
}

// chmodModeArtifact changes only this held inode. The fallback follows a
// verified procfs magic link to the original O_PATH, never a store pathname.
// This is needed on supported kernels older than fchmodat2's AT_EMPTY_PATH.
func chmodModeArtifact(file *os.File, mode os.FileMode) error {
	if mode != 0o111 && mode != 0o555 {
		return fmt.Errorf("unsupported runtime mode transition")
	}
	err := unix.Fchmodat(int(file.Fd()), "", uint32(mode), unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EBADF) {
		return fmt.Errorf("change held runtime inode mode: %w", err)
	}
	var proc unix.Statfs_t
	if err := unix.Statfs("/proc/self/fd", &proc); err != nil {
		return err
	}
	if proc.Type != unix.PROC_SUPER_MAGIC {
		return fmt.Errorf("runtime metadata fallback is not original procfs")
	}
	if err := os.Chmod("/proc/self/fd/"+strconv.FormatUint(uint64(file.Fd()), 10), mode); err != nil {
		return fmt.Errorf("change pinned runtime metadata: %w", err)
	}
	return nil
}

func sameModeArtifact(ref cachefs.Reference, root *os.Root, item Item, file *os.File, mode os.FileMode) error {
	if !ref.Known() || ref.Directory || ref.Mode != 0o555 || ref.Size != item.Size {
		return fmt.Errorf("runtime execution intent has another artifact role")
	}
	if err := ref.CheckCreatedFile(root, item.SHA256, file, mode); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != ref.Size || !info.ModTime().Equal(ref.Modified) {
		return fmt.Errorf("runtime mode artifact lost its original content metadata")
	}
	return nil
}

// A LIVE mode owner already pinned this exact validated inode. An atomic
// rename/unlink after capture must neither redirect execution nor make a
// restore chmod the replacement pathname. Recovery without that live native
// owner continues to require the current anchored name and original intent.
func checkHeldModeArtifact(ref cachefs.Reference, item Item, file *os.File, mode os.FileMode) error {
	if !ref.Known() || ref.Directory || ref.Mode != 0o555 {
		return fmt.Errorf("held runtime has no original native role")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	identity := fmt.Sprintf("unix:%v:%v:%v", stat.Dev, stat.Ino, stat.Uid)
	if identity != ref.Identity || stat.Nlink > 1 || info.Mode() != mode || info.Size() != item.Size || info.Size() != ref.Size || !info.ModTime().Equal(ref.Modified) {
		return fmt.Errorf("held runtime lost its original immutable inode")
	}
	return nil
}

func openModeReadable(root *os.Root, item Item, metadata *os.File) (*os.File, error) {
	file, err := cachefs.OpenFile(root, item.SHA256, os.O_RDONLY, 0o555)
	if err != nil {
		return nil, err
	}
	a, err := metadata.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	b, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(a, b) {
		file.Close()
		return nil, fmt.Errorf("mode artifact was replaced during readable open")
	}
	return file, nil
}
