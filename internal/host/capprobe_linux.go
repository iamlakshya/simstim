//go:build linux

package host

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	kvmDevicePath    = "/dev/kvm"
	kvmGetAPIVersion = 0xae00
	kvmAPIVersion    = 12
)

func probeKVM(uid int, u *user.User) api.KVM {
	fi, err := os.Stat(kvmDevicePath)
	if err != nil {
		return api.KVM{}
	}
	k := api.KVM{
		Present: true,
		Mode:    posixModeString(fi.Mode()),
	}
	var gid int
	var hasGid bool
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		gid = int(st.Gid)
		hasGid = true
		if gr, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil && gr != nil {
			k.Group = gr.Name
		}
	}

	if fd, err := unix.Open(kvmDevicePath, unix.O_RDWR|unix.O_CLOEXEC, 0); err == nil {
		ver, ioErr := unix.IoctlRetInt(fd, kvmGetAPIVersion)
		_ = unix.Close(fd)
		k.Usable = ioErr == nil && ver == kvmAPIVersion
	}

	if uid >= 0 {
		k.ACLUser = hasKVMUserACL(kvmDevicePath, uint32(uid)) //nolint:gosec // G115: uid checked non-negative
	}

	if hasGid {
		gidStr := strconv.Itoa(gid)
		if u != nil {
			if gids, err := u.GroupIds(); err == nil {
				for _, g := range gids {
					if g == gidStr {
						k.InGroup = true
						break
					}
				}
			}
		}
		if nowGroups, err := os.Getgroups(); err == nil {
			k.InGroupNow = slices.Contains(nowGroups, gid)
		}
		k.InGroupManager = userManagerInGroup(gid)
	}
	return k
}

func hasKVMUserACL(path string, uid uint32) bool {
	buf := make([]byte, 4096)
	n, err := unix.Getxattr(path, "system.posix_acl_access", buf)
	if err != nil || n <= 0 {
		return false
	}
	return parsePOSIXACLAccess(buf[:n], uid)
}

func userManagerInGroup(gid int) bool {
	procsPath := filepath.Join(userManagerCgroup(), "init.scope", "cgroup.procs")
	b, err := os.ReadFile(procsPath)
	if err != nil {
		return false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return false
	}
	root, err := os.OpenRoot(procRoot)
	if err != nil {
		return false
	}
	defer root.Close()
	status, err := root.ReadFile(filepath.Join(strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	return slices.Contains(parseProcStatusGroups(status), gid)
}

func posixModeString(m os.FileMode) string {
	var b [10]byte
	switch {
	case m&os.ModeCharDevice != 0:
		b[0] = 'c'
	case m&os.ModeDevice != 0:
		b[0] = 'b'
	case m.IsDir():
		b[0] = 'd'
	case m&os.ModeSymlink != 0:
		b[0] = 'l'
	default:
		b[0] = '-'
	}
	perm := m.Perm()
	const rwx = "rwx"
	for i := range 9 {
		if perm&(1<<uint(8-i)) != 0 {
			b[1+i] = rwx[i%3]
		} else {
			b[1+i] = '-'
		}
	}
	return string(b[:])
}

func probeDiskFS(dir string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return ""
	}
	switch uint64(st.Type) { //nolint:gosec // G115: filesystem magic constants are unsigned 32/64-bit identifiers
	case 0x58465342:
		return "xfs"
	case 0xef53:
		return "ext4"
	case 0x9123683e:
		return "btrfs"
	case 0x01021994:
		return "tmpfs"
	case 0x2fc12fc1:
		return "zfs"
	case 0x794c7630:
		return "overlayfs"
	case 0xf2f52010:
		return "f2fs"
	default:
		return fmt.Sprintf("0x%x", uint64(st.Type)) //nolint:gosec // G115: see above
	}
}

func probeReflink(incomingDir string) bool {
	if err := os.MkdirAll(incomingDir, 0o700); err != nil {
		return false
	}
	src, err := os.CreateTemp(incomingDir, ".reflink-src-*")
	if err != nil {
		return false
	}
	srcName := src.Name()
	defer os.Remove(srcName)
	defer src.Close()

	var block [4096]byte
	block[0] = 'r'
	if _, err := src.Write(block[:]); err != nil {
		return false
	}
	dst, err := os.CreateTemp(incomingDir, ".reflink-dst-*")
	if err != nil {
		return false
	}
	dstName := dst.Name()
	defer os.Remove(dstName)
	defer dst.Close()

	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())) == nil
}

func readRootOwnedFile(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return nil, false
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}
