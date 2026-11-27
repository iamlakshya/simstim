//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"golang.org/x/sys/unix"
)

type slotRecord struct {
	Slot    int             `json:"slot"`
	Session string          `json:"session"`
	UID     int             `json:"uid"`
	GID     int             `json:"gid"`
	SlotUID int             `json:"slot_uid"`
	SlotGID int             `json:"slot_gid"`
	BootID  string          `json:"boot_id"`
	Cgroup  *cgroupIdentity `json:"cgroup,omitempty"`
}
type slotAccount struct {
	uid, gid int
	groups   []uint32
}
type keeperPlan struct {
	Request   Request        `json:"request"`
	Slot      int            `json:"slot"`
	SlotUID   int            `json:"slot_uid"`
	GID       int            `json:"gid"`
	SlotGID   int            `json:"slot_gid"`
	Cgroup    cgroupIdentity `json:"cgroup"`
	FreshDisk bool           `json:"fresh_disk"`
}

func stageArgs(stage string) []string { return []string{brand.Name + "-slot", stage} }

func (b *backend) start(ctx context.Context, r Request) (Response, error) {
	return b.startWithInputsV2(ctx, r, nil)
}

type rootStartInputsV2 struct {
	reserved []*os.File
	fork     *os.File
}

func (b *backend) startWithInputsV2(ctx context.Context, r Request, input *rootStartInputsV2) (Response, error) {
	// Unsupported native drivers fail before taking any session or slot.
	if r.Driver == "nitro" {
		device, err := nitrodev.Open()
		if err != nil {
			return Response{}, nitroStartError(err)
		}
		if err := device.Close(); err != nil {
			return Response{}, err
		}
	}
	accounts, err := slotAccounts()
	if err != nil {
		return Response{}, err
	}
	if err := checkCallerSlotGroups(accounts, b.caller.gid); err != nil {
		return Response{}, err
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(cgroup) > RequestMax || !strings.Contains(string(cgroup), "0::/") {
		return Response{}, refused("the launcher has no current unified session cgroup")
	}
	current, err := os.Readlink("/proc/self/exe")
	if err != nil || current != LauncherPath() {
		return Response{}, refused("the launcher is not running from its fixed root-owned installation")
	}
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return Response{}, err
	}
	defer self.Close()
	selfStat, err := self.Stat()
	if err != nil || !singleRegular(selfStat, 0) || selfStat.Mode().Perm() != 0o755 {
		return Response{}, refused("the executing launcher is not a protected release executable")
	}
	record, sessionLock, err := b.session(r.Session, true)
	if err != nil {
		return Response{}, err
	}
	defer sessionLock.Close()
	if input != nil && len(input.reserved) > 0 {
		if len(input.reserved) != 19 {
			return Response{}, refused("the reserved new root has no exact old reservation and new parent cgroup")
		}
		if err := errors.Join(b.checkReservedSlotRolesV2(record, input.reserved[:18]), requireOriginalSlotActorsExitedV2(input.reserved[:18]), checkCurrentStartParentV2(input.reserved[18])); err != nil {
			return Response{}, err
		}
	} else if err := b.stopped(r.Session); err != nil {
		return Response{}, err
	}
	storeLock, err := b.lock("store/.lock", false)
	if err != nil {
		return Response{}, err
	}
	defer storeLock.Close()
	defer func() { _ = b.refreshCapacityLocked() }()
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for _, digest := range r.digests() {
		file, err := b.open("store/sha256/"+digest, unix.O_RDONLY, 0)
		if err != nil {
			return Response{}, err
		}
		files = append(files, file)
		if err := b.checkStore(file, digest, 0); err != nil {
			return Response{}, err
		}
	}
	var slot int
	var slotLock *os.File
	if input != nil && len(input.reserved) > 0 {
		slot = *record.Slot
		if err := b.checkSlotLeaseInodeV2(slot, input.reserved[17]); err != nil {
			return Response{}, err
		}
		slotLock, err = dupFile(input.reserved[17])
	} else {
		slot, slotLock, err = b.acquireSlot()
	}
	if err != nil {
		return Response{}, err
	}
	defer slotLock.Close()
	if err := files[0].Chmod(0o555); err != nil {
		return Response{}, err
	}
	account := accounts[slot]
	if input != nil && len(input.reserved) > 0 && account.uid != record.SlotUID {
		return Response{}, refused("the original reserved slot account changed before new creation")
	}
	if account.uid == b.caller.uid {
		return Response{}, refused("a slot account aliases the login caller")
	}
	if err := purgeSlot(ctx, account); err != nil {
		return Response{}, err
	}
	if input == nil || len(input.reserved) == 0 {
		if err := b.recordVMPreparation(record, account.uid, r.digests()); err != nil {
			return Response{}, err
		}
	}
	vmDir, disk, fresh, err := b.prepareVM(r, account.uid)
	if err != nil {
		return Response{}, err
	}
	defer vmDir.Close()
	defer disk.Close()
	if fresh {
		defer func() { _ = b.removeCapturedFile(disk.Name(), disk) }()
	}
	if input != nil && len(input.reserved) > 0 {
		got, err := disk.Stat()
		if err != nil {
			return Response{}, err
		}
		want, err := input.reserved[9].Stat()
		if err != nil || fresh || !os.SameFile(want, got) {
			return Response{}, refused("the reserved new keeper did not receive the SAME completed current disk")
		}
	}
	if input != nil && input.fork != nil {
		if !fresh {
			return Response{}, refused("fork data needs its actual fresh root-created child target")
		}
		if err := b.copyOriginalForkInputV2(ctx, r, input.fork, disk, account.uid); err != nil {
			return Response{}, err
		}
	}
	cgroupFile, cgroupIdentity, err := makeCgroup(string(cgroup), slot)
	if err != nil {
		return Response{}, err
	}
	defer cgroupFile.Close()
	bootID, err := nativeBootID()
	if err != nil {
		return Response{}, err
	}
	slotMeta := slotRecord{Slot: slot, Session: r.Session, UID: b.caller.uid, GID: b.caller.gid, SlotUID: account.uid, SlotGID: account.gid, BootID: bootID, Cgroup: &cgroupIdentity}
	if err := b.writeJSON(fmt.Sprintf("slots/%02d.json", slot), slotMeta); err != nil {
		return Response{}, err
	}
	record.Digests, record.Slot, record.SlotUID = r.digests(), &slot, account.uid
	if err := b.writeJSON("sessions/"+r.Session+".json", record); err != nil {
		return Response{}, err
	}
	controlPath := RootPath() + "/sessions/" + r.Session + ".sock"
	if err := b.root.Remove("sessions/" + r.Session + ".sock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Response{}, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: controlPath, Net: "unix"})
	if err != nil {
		return Response{}, err
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := os.Chmod(controlPath, 0o600); err != nil {
		return Response{}, err
	}
	control, err := listener.File()
	if err != nil {
		return Response{}, err
	}
	defer control.Close()
	planR, planW, err := os.Pipe()
	if err != nil {
		return Response{}, err
	}
	defer planR.Close()
	defer planW.Close()
	replyPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return Response{}, err
	}
	replyR, replyW := os.NewFile(uintptr(replyPair[0]), "root-keeper-receive"), os.NewFile(uintptr(replyPair[1]), "keeper-native-send")
	if err := unix.SetsockoptInt(replyPair[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		replyR.Close()
		replyW.Close()
		return Response{}, err
	}
	defer replyR.Close()
	defer replyW.Close()
	// The child is this exact signed executable, never a store program. The
	// root transition happens in the kernel before any keeper plan is read.
	command := exec.Command("/proc/self/exe")
	command.Args = stageArgs("keeper-v1")
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	command.ExtraFiles = append([]*os.File{planR, replyW, slotLock, control}, files...)
	command.ExtraFiles = append(command.ExtraFiles, cgroupFile, vmDir, disk)
	pidfd := -1
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: uint32(account.uid), Gid: uint32(account.gid), Groups: account.groups}, PidFD: &pidfd, UseCgroupFD: true, CgroupFD: int(cgroupFile.Fd())}
	if err := command.Start(); err != nil {
		return Response{}, err
	}
	defer unix.Close(pidfd)
	planR.Close()
	replyW.Close()
	failed := true
	defer func() {
		if failed {
			_ = killNativeCgroup(context.Background(), cgroupFile)
			_ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
			_ = command.Wait()
		}
	}()
	formatFresh := fresh && (input == nil || input.fork == nil)
	if err := json.NewEncoder(planW).Encode(keeperPlan{Request: r, Slot: slot, SlotUID: account.uid, GID: b.caller.gid, SlotGID: account.gid, Cgroup: cgroupIdentity, FreshDisk: formatFresh}); err != nil {
		return Response{}, err
	}
	planW.Close()
	replyConnection, err := net.FileConn(replyR)
	if err != nil {
		return Response{}, err
	}
	defer replyConnection.Close()
	if err := replyConnection.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return Response{}, err
	}
	response, nativeFiles, credential, err := receiveNative(replyConnection.(*net.UnixConn))
	defer func() {
		for _, file := range nativeFiles {
			file.Close()
		}
	}()
	if err != nil || len(nativeFiles) != 2 || credential == nil || int(credential.Uid) != account.uid || int(credential.Pid) != command.Process.Pid {
		return Response{}, refused("the slot keeper did not confirm its native jail")
	}
	if _, err := nativePID(nativeFiles[0]); err != nil || !pidLive(nativeFiles[0]) {
		return Response{}, refused("the keeper supplied no original live namespace init")
	}
	if err := checkSlotVMM(nativeFiles[1], r.VMM); err != nil {
		return Response{}, err
	}
	if !response.OK || response.PID <= 0 || response.Slot == nil || *response.Slot != slot {
		return Response{}, refused("the slot keeper refused its native jail")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if fresh {
		if err := b.publishFreshDisk(r.Session, disk); err != nil {
			return Response{}, err
		}
	}
	// The live keeper now owns the jail's creation pidfd and the original
	// lock descriptor. Closing this invocation cannot release that lease.
	if _, err := unix.GetsockoptInt(1, unix.SOL_SOCKET, unix.SO_TYPE); err == nil {
		response.native, err = transferForStart(pidfd, nativeFiles[0], nativeFiles[1], cgroupFile, files, disk, replyR, self)
		if err != nil {
			return Response{}, err
		}
	}
	failed = false
	_ = command.Process.Release()
	return response, nil
}

func (b *backend) acquireSlot() (int, *os.File, error) {
	quarantined := false
	for i := range SlotCount {
		candidate, err := b.lock(slotLockName(i), true)
		if errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return -1, nil, err
		}
		if err := b.vacancy(i); err != nil {
			candidate.Close()
			quarantined = true
			continue
		}
		return i, candidate, nil
	}
	if quarantined {
		return -1, nil, refused("no vacant slot has a complete native fence; uncertain slots remain quarantined")
	}
	return -1, nil, &launchError{Code: "no_room", Message: "there is no free slot: 16 sandboxes run under accounts of their own", Hint: "List the host's sessions, then stop one."}
}

func nitroStartError(cause error) error {
	if strings.Contains(cause.Error(), "Failed to query hypervisor-supported flags") || strings.Contains(cause.Error(), "required Nitro") {
		return &launchError{Code: "wall_unavailable", Message: "the parent Nitro driver lacks the sandbox interface", Hint: nitrodev.ParentKernelHint}
	}
	return refused("the original Nitro device is unavailable: " + cause.Error())
}
