package host

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func readPlatformCapabilities(ctx context.Context, s capSource, p *api.CapProbe) {
	first := strings.Fields(capOutput(ctx, s, "systemctl", "--version"))
	if len(first) > 1 && first[0] == "systemd" {
		p.Systemd, _ = strconv.Atoi(first[1])
	}
	if s.lookup("systemctl") {
		_, err := s.run(ctx, "systemctl", "--user", "show-environment")
		p.UserManager = err == nil
	}
	p.Linger = strings.TrimSpace(capOutput(ctx, s, "loginctl", "show-user", s.username, "-p", "Linger", "--value")) == "yes"
	_, err := os.Stat(s.path("/sys/fs/cgroup/cgroup.controllers"))
	p.CgroupV2 = err == nil
	manager := filepath.Join("/sys/fs/cgroup/user.slice", fmt.Sprintf("user-%d.slice", s.uid), fmt.Sprintf("user@%d.service", s.uid))
	p.Delegated = probeControllers(s.read(filepath.Join(manager, "cgroup.controllers")))
	p.KVM = probeKVM(s, manager)
	p.Seccomp = probeStatusSeccomp(s.read("/proc/self/status"))
	p.UserNSRestricted = probeInt(s.read("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")) > 0
	p.IOURingDisabled = probeInt(s.read("/proc/sys/kernel/io_uring_disabled"))
	p.VsockBlocked = probeVsockBlocked(s.read(filepath.Join("/etc/modprobe.d", "50-"+brand.Name+"-vsock.conf")))
	p.UserNSCopyCurrent = probeUserNSCopy(s)
	p.Nitro = probeNitro(s)
}

func probeInt(b []byte) int { n, _ := strconv.Atoi(strings.TrimSpace(string(b))); return n }

func probeKVM(s capSource, manager string) api.KVM {
	path := s.path("/dev/kvm")
	st, err := os.Stat(path)
	if err != nil {
		return api.KVM{}
	}
	k := api.KVM{Present: true, Mode: st.Mode().String()}
	var sys unix.Stat_t
	if unix.Stat(path, &sys) != nil {
		return k
	}
	gid := strconv.FormatUint(uint64(sys.Gid), 10)
	k.Group = gid
	if group, err := user.LookupGroupId(gid); err == nil {
		k.Group = group.Name
	}
	k.InGroup = slices.Contains(s.accountGroups, gid)
	k.InGroupNow = slices.Contains(s.processGroups, gid)
	pids := strings.Fields(string(s.read(filepath.Join(manager, "init.scope/cgroup.procs"))))
	if len(pids) > 0 {
		if pid, err := strconv.Atoi(pids[0]); err == nil && pid > 0 {
			k.InGroupManager = slices.Contains(probeStatusGroups(s.read(fmt.Sprintf("/proc/%d/status", pid))), gid)
		}
	}
	if uid, ok := capabilityUID(s.uid); ok {
		k.ACLUser = probeACLUser(probeXattr(path), uid)
	}
	k.Usable = probeDeviceOpen(path, true)
	return k
}

func probeXattr(path string) []byte {
	n, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	if err != nil || n < 4 || n > 1<<20 {
		return nil
	}
	b := make([]byte, n)
	n, err = unix.Getxattr(path, "system.posix_acl_access", b)
	if err != nil {
		return nil
	}
	return b[:n]
}

func probeDeviceOpen(path string, kvm bool) bool {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	if !kvm {
		return true
	}
	result, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0xae00, 0)
	return errno == 0 && result == 12
}

func probeNitro(s capSource) *api.Nitro {
	path := s.path("/dev/nitro_enclaves")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	n := &api.Nitro{Present: true, Usable: probeDeviceOpen(path, false), Pools: map[string]int64{}}
	if uid, ok := capabilityUID(s.uid); ok {
		n.ACLUser = probeACLUser(probeXattr(path), uid)
	}
	base := "/sys/class/misc/nitro_enclaves"
	for key, file := range map[string]string{"cpu_pool": "ne_cpus", "mem_pool": "mem_pool/size", "pcie_slots": "pcie_slots"} {
		value, err := strconv.ParseInt(strings.TrimSpace(string(s.read(filepath.Join(base, file)))), 10, 64)
		if err == nil && value >= 0 {
			n.Pools[key] = value
		}
	}
	n.Pools["vcpus"] = int64(len(probeCPUList(s.read("/sys/devices/system/cpu/online"))))
	return n
}

func probeCPUList(b []byte) []int {
	var cpus []int
	for _, part := range strings.Split(strings.TrimSpace(string(b)), ",") {
		lo, hi, rangePart := strings.Cut(part, "-")
		first, err := strconv.Atoi(lo)
		if err != nil || first < 0 {
			return nil
		}
		last := first
		if rangePart {
			last, err = strconv.Atoi(hi)
			if err != nil || last < first || last-first > 1<<16 {
				return nil
			}
		}
		if len(cpus)+last-first+1 > 1<<16 {
			return nil
		}
		for cpu := first; cpu <= last; cpu++ {
			cpus = append(cpus, cpu)
		}
	}
	return cpus
}

func probeVsockBlocked(b []byte) bool {
	blocked := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		words := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(words) == 2 && words[0] == "blacklist" {
			blocked[words[1]] = true
		}
		if len(words) == 3 && words[0] == "install" && (words[2] == "/bin/false" || words[2] == "/usr/bin/false") {
			blocked[words[1]] = true
		}
	}
	return blocked["vsock_loopback"] && blocked["vmw_vsock_virtio_transport"] && blocked["vhost_vsock"]
}

func probeUserNSCopy(s capSource) bool {
	copyPath := s.path(filepath.Join("/usr/local/libexec", brand.Name, "userns"))
	if !trustedRootFile(copyPath) {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	installed, err := readProbeFile(copyPath, 16<<20)
	if err != nil {
		return false
	}
	shipped, err := readProbeFile(filepath.Join(filepath.Dir(exe), "userns"), 16<<20)
	return err == nil && sha256.Sum256(installed) == sha256.Sum256(shipped)
}

func trustedRootFile(path string) bool {
	for current := path; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o022 != 0 {
			return false
		}
		var sys unix.Stat_t
		if unix.Lstat(current, &sys) != nil || sys.Uid != 0 {
			return false
		}
		if current == path {
			if !st.Mode().IsRegular() {
				return false
			}
		} else if !st.IsDir() {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return true
}

func probeDisk(dataDir, incoming string) (int64, string, bool) {
	path := dataDir
	var st unix.Statfs_t
	for unix.Statfs(path, &st) != nil {
		parent := filepath.Dir(path)
		if parent == path {
			return 0, "", false
		}
		path = parent
	}
	if st.Bsize <= 0 || st.Bavail > uint64(math.MaxInt64)/uint64(st.Bsize) {
		return 0, "", false
	}
	free := int64(st.Bavail) * st.Bsize
	fs := fmt.Sprintf("0x%x", st.Type)
	switch st.Type {
	case unix.XFS_SUPER_MAGIC:
		fs = "xfs"
	case unix.BTRFS_SUPER_MAGIC:
		fs = "btrfs"
	case unix.EXT4_SUPER_MAGIC:
		fs = "ext4"
	case unix.TMPFS_MAGIC:
		fs = "tmpfs"
	case unix.OVERLAYFS_SUPER_MAGIC:
		fs = "overlay"
	}
	dirFD, err := openProbeIncoming(dataDir, incoming)
	if err != nil {
		return free, fs, false
	}
	defer unix.Close(dirFD)
	scratchDir := fmt.Sprintf("/proc/self/fd/%d", dirFD)
	a, err := os.CreateTemp(scratchDir, ".reflink-source-*")
	if err != nil {
		return free, fs, false
	}
	defer os.Remove(a.Name())
	defer a.Close()
	b, err := os.CreateTemp(scratchDir, ".reflink-target-*")
	if err != nil {
		return free, fs, false
	}
	defer os.Remove(b.Name())
	defer b.Close()
	if _, err := a.WriteString("reflink probe\n"); err != nil {
		return free, fs, false
	}
	return free, fs, unix.IoctlFileClone(int(b.Fd()), int(a.Fd())) == nil
}

// openProbeIncoming opens every component beneath the chosen data directory
// without following links. It never creates dataDir or artefacts.
func openProbeIncoming(dataDir, incoming string) (int, error) {
	if filepath.Clean(incoming) != filepath.Join(filepath.Clean(dataDir), "artefacts", "incoming") {
		return -1, errors.New("unexpected incoming folder")
	}
	data, err := unix.Open(dataDir, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open data folder: %w", err)
	}
	defer unix.Close(data)
	artefacts, err := unix.Openat(data, "artefacts", unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open artefacts folder: %w", err)
	}
	defer unix.Close(artefacts)
	// A missing incoming folder is the only directory the scratch test makes.
	if err := unix.Mkdirat(artefacts, "incoming", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("make incoming folder: %w", err)
	}
	fd, err := unix.Openat(artefacts, "incoming", unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open incoming folder: %w", err)
	}
	return fd, nil
}

func capabilityUID(uid int) (uint32, bool) {
	if uid < 0 {
		return 0, false
	}
	value := uint64(uid)
	if value > math.MaxUint32 {
		return 0, false
	}
	return uint32(value), true
}
