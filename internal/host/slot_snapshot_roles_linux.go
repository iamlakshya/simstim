//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func duplicateSlotSnapshotFileV1(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, rewindFailureV1("an original Snapshot native handle is absent")
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "original-outside-snapshot"), nil
}

func slotSnapshotPIDV1(file *os.File, live bool) (int, error) {
	if file == nil {
		return 0, rewindFailureV1("an original Snapshot process handle is absent")
	}
	if err := unix.PidfdSendSignal(int(file.Fd()), 0, nil, 0); err != nil && (live || !errors.Is(err, unix.ESRCH)) {
		return 0, err
	}
	polls := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || live && count != 0 || !live && (count != 1 || polls[0].Revents&unix.POLLIN == 0) {
		return 0, errors.Join(rewindFailureV1("the original Snapshot process has another native lifetime"), err)
	}
	if !live {
		return 0, nil
	}
	raw, err := ordinaryFDInfoV1(file)
	if err != nil {
		return 0, err
	}
	pid := 0
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Pid:" {
			continue
		}
		if len(fields) != 2 || pid != 0 {
			return 0, rewindFailureV1("the original Snapshot pidfd has ambiguous native identity")
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return 0, rewindFailureV1("the original Snapshot pidfd has no live host identity")
		}
	}
	if pid <= 0 {
		return 0, rewindFailureV1("the original Snapshot role is not a native pidfd")
	}
	return pid, nil
}

func slotSnapshotGroupV1(file *os.File) error {
	if file == nil {
		return rewindFailureV1("the original Snapshot group ended")
	}
	var native unix.Stat_t
	var filesystem unix.Statfs_t
	if err := unix.Fstat(int(file.Fd()), &native); err != nil {
		return err
	}
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC || native.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.Join(rewindFailureV1("the original Snapshot group is not a native cgroup2 directory"), err)
	}
	return nil
}

func (owner *SlotSnapshotOwnerV1) checkRolesV1(ctx context.Context) error {
	if ctx == nil || owner == nil || owner.issuer != owner || owner.warm == nil || len(owner.roles) != 17 {
		return rewindFailureV1("the original outside Snapshot source is absent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	first := owner.roleInfo == nil
	if first {
		owner.roleInfo = make([]os.FileInfo, len(owner.roles))
	}
	pids := map[int]bool{}
	for _, index := range []int{0, 1, 14} {
		pid, err := slotSnapshotPIDV1(owner.roles[index], true)
		if err != nil {
			return err
		}
		if pids[pid] {
			return rewindFailureV1("the original Snapshot keeper, init and VMM roles alias")
		}
		pids[pid] = true
	}
	if err := errors.Join(slotSnapshotGroupV1(owner.roles[2]), slotSnapshotGroupV1(owner.roles[13]), slotSnapshotGroupV1(owner.group)); err != nil {
		return err
	}
	parent, err := owner.roles[13].Stat()
	if err != nil {
		return err
	}
	original, err := owner.group.Stat()
	if err != nil || !os.SameFile(parent, original) {
		return errors.Join(rewindFailureV1("the slot Snapshot has another original supervisor group"), err)
	}
	fd, err := unix.Openat(int(owner.roles[2].Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	actualParent := os.NewFile(uintptr(fd), "original-snapshot-slot-parent")
	actual, statErr := actualParent.Stat()
	closeErr := actualParent.Close()
	if statErr != nil || !os.SameFile(actual, parent) {
		return errors.Join(rewindFailureV1("the original slot group has another actual native parent"), statErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	for index, file := range owner.roles {
		if file == nil {
			return rewindFailureV1("the original slot Snapshot has a missing native role")
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if first {
			owner.roleInfo[index] = info
		} else if !os.SameFile(owner.roleInfo[index], info) || index >= 3 && index <= 9 && info.Size() != owner.roleInfo[index].Size() {
			return rewindFailureV1("an original slot Snapshot role changed identity or size")
		}
		if index >= 4 && index <= 8 {
			native, ok := info.Sys().(*syscall.Stat_t)
			flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			if !ok || native.Uid != 0 || native.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 || flagErr != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
				return errors.Join(rewindFailureV1("an original immutable slot role is writable or has another creator"), flagErr)
			}
		}
	}
	launcher, err := owner.roles[11].Stat()
	if err != nil {
		return err
	}
	rootCode, ok := launcher.Sys().(*syscall.Stat_t)
	if !ok || rootCode.Uid != 0 || rootCode.Nlink != 1 || !launcher.Mode().IsRegular() || launcher.Mode().Perm() != 0o755 {
		return rewindFailureV1("the original Snapshot root issuer code changed its native role")
	}
	// The original root sender may already have exited. Validate the retained
	// native kind without opening a fresh process by its former PID.
	if err := unix.PidfdSendSignal(int(owner.roles[12].Fd()), 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	kind, err := unix.GetsockoptInt(int(owner.roles[10].Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_STREAM {
		return errors.Join(rewindFailureV1("the original keeper status role is not its native stream"), err)
	}
	if first {
		if err := owner.checkBootBytesV1(ctx); err != nil {
			return err
		}
	}
	disk, err := owner.roles[9].Stat()
	if err != nil {
		return err
	}
	native, ok := disk.Sys().(*syscall.Stat_t)
	if !ok || !disk.Mode().IsRegular() || native.Nlink != 1 || disk.Size() != owner.need.DiskMax || disk.Mode().Perm() != 0o660 {
		return rewindFailureV1("the original root-created slot disk changed its fixed role")
	}
	dir, err := owner.roles[16].Stat()
	if err != nil {
		return err
	}
	native, ok = dir.Sys().(*syscall.Stat_t)
	if !ok || !dir.IsDir() || native.Uid != 0 || dir.Mode().Perm() != 0o700 {
		return rewindFailureV1("the original checkpoint directory has another native creator or mode")
	}
	for _, role := range []struct {
		file *os.File
		kind int
		path string
	}{{owner.user, unix.CLONE_NEWUSER, "/proc/self/ns/user"}, {owner.cgroup, unix.CLONE_NEWCGROUP, "/proc/self/ns/cgroup"}} {
		if role.file == nil {
			return rewindFailureV1("the original supervisor namespace handle ended")
		}
		var filesystem unix.Statfs_t
		kind, err := unix.IoctlRetInt(int(role.file.Fd()), unix.NS_GET_NSTYPE)
		if unix.Fstatfs(int(role.file.Fd()), &filesystem) != nil || filesystem.Type != unix.NSFS_MAGIC || err != nil || kind != role.kind {
			return rewindFailureV1("the original supervisor namespace changed native type")
		}
		self, err := os.Open(role.path)
		if err != nil {
			return err
		}
		a, statErr := self.Stat()
		b, peerErr := role.file.Stat()
		closeErr := self.Close()
		if statErr != nil || peerErr != nil || !os.SameFile(a, b) {
			return errors.Join(rewindFailureV1("the original supervisor left its accepted host namespace"), statErr, peerErr, closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	kind, err = unix.GetsockoptInt(int(owner.roles[15].Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	peer, peerErr := unix.GetsockoptUcred(int(owner.roles[15].Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	pid, pidErr := slotSnapshotPIDV1(owner.roles[14], true)
	if err != nil || peerErr != nil || pidErr != nil || kind != unix.SOCK_STREAM || peer == nil || int(peer.Pid) != pid || peer.Uid != disk.Sys().(*syscall.Stat_t).Uid {
		return errors.Join(rewindFailureV1("the original Snapshot guest endpoint differs from its actual VMM birth handle"), err, peerErr, pidErr)
	}
	return ctx.Err()
}

func (owner *SlotSnapshotOwnerV1) checkBootBytesV1(ctx context.Context) error {
	name := "cloud-hypervisor"
	if owner.need.Driver == "firecracker" || owner.need.Driver == "nitro" {
		name = "firecracker"
	}
	for _, role := range []struct {
		index      int
		kind, name string
	}{{4, supply.KindVMM, name}, {5, supply.KindKernel, "guest"}, {6, supply.KindTools, "tools"}} {
		item, ok := owner.need.Runtime.Find(role.kind, role.name, runtime.GOARCH)
		if !ok {
			return rewindFailureV1("the original slot Snapshot lost a complete runtime role")
		}
		if err := slotSnapshotContentV1(ctx, owner.roles[role.index], item.Size, item.SHA256); err != nil {
			return err
		}
	}
	if err := slotSnapshotContentV1(ctx, owner.roles[8], owner.need.Agents.Size, owner.need.Agents.SHA256); err != nil {
		return err
	}
	// The executed sealed object is correlated with the admitted VMM bytes;
	// root inspectGuest independently checks its actual map and RO projection.
	info, err := owner.roles[4].Stat()
	if err != nil {
		return err
	}
	item, _ := owner.need.Runtime.Find(supply.KindVMM, name, runtime.GOARCH)
	if err := slotSnapshotContentV1(ctx, owner.roles[3], info.Size(), item.SHA256); err != nil {
		return err
	}
	seals, err := unix.FcntlInt(owner.roles[3].Fd(), unix.F_GET_SEALS, 0)
	want := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if err != nil || seals&want != want {
		return errors.Join(rewindFailureV1("the original executed VMM lacks its actual immutable native seals"), err)
	}
	return nil
}

func slotSnapshotContentV1(ctx context.Context, file *os.File, size int64, digest string) error {
	info, err := file.Stat()
	if err != nil || size <= 0 || info.Size() != size {
		return errors.Join(rewindFailureV1("an original slot Snapshot boot role has another fixed byte length"), err)
	}
	hash := sha256.New()
	reader := io.NewSectionReader(file, 0, size)
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return rewindFailureV1("an original slot Snapshot boot role differs from the protected complete Need")
	}
	return ctx.Err()
}

func (owner *SlotSnapshotOwnerV1) checkPhysicalExitV1() error {
	if owner == nil || owner.issuer != owner || len(owner.roles) != 17 {
		return rewindFailureV1("the stopped Snapshot has no original native source")
	}
	for _, file := range []*os.File{owner.supervisor, owner.roles[0], owner.roles[1], owner.roles[14]} {
		if _, err := slotSnapshotPIDV1(file, false); err != nil {
			return err
		}
	}
	for _, file := range []*os.File{owner.group, owner.roles[2]} {
		if err := slotSnapshotGroupV1(file); err != nil {
			return err
		}
		empty, err := originalStopCgroupEmptyV1(file)
		if err != nil || !empty {
			return errors.Join(rewindFailureV1("the original Snapshot group has no positive whole-group vacancy"), err)
		}
	}
	return nil
}
