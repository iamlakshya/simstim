//go:build linux

package slotlaunch

import (
	"context"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// inspectGuest receives only the actual socket, original init birth handle
// and original cgroup descriptor. No request PID, uid or path is accepted.
func (b *backend) inspectGuest(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("native guest inspection needs its original Unix descriptor channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	if record.Slot == nil || len(record.Digests) != 5 {
		return Response{}, refused("the native guest has no complete live slot allocation")
	}
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(*record.Slot), &allocation); err != nil {
		return Response{}, err
	}
	boot, err := nativeBootID()
	if err != nil || allocation.Session != r.Session || allocation.UID != b.caller.uid || allocation.BootID != boot || allocation.Cgroup == nil {
		return Response{}, refused("the native guest's root allocation ended or changed")
	}
	// The checkpoint client retains this actual root worker's birth handle
	// before any disk operation may begin. Cancellation then joins THIS
	// creator, independently of a sudo monitor's status or an ended pipe.
	if r.Op == "checkpoint" || r.Op == "checkpoint.reserve-v2" {
		initial, err := checkpointNativeReplyV1(Response{OK: true})
		if err != nil {
			return Response{}, err
		}
		err = sendNativeResponse(initial)
		initial.native.close()
		if err != nil {
			return Response{}, err
		}
	} else {
		// Ack before recvmsg prevents JSON read-ahead from consuming rights.
		if _, err := unix.Write(1, []byte{1}); err != nil {
			return Response{}, err
		}
	}
	control := make([]byte, unix.CmsgSpace(7*4))
	marker := make([]byte, 1)
	n, cn, flags, _, err := unix.Recvmsg(0, marker, control, unix.MSG_CMSG_CLOEXEC)
	if err != nil || n != 1 || marker[0] != 1 || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
		closeReceivedRights(control[:cn])
		return Response{}, refused("the native guest handoff was truncated")
	}
	messages, err := unix.ParseSocketControlMessage(control[:cn])
	if err != nil {
		return Response{}, err
	}
	var inputs []*os.File
	defer func() {
		for _, file := range inputs {
			file.Close()
		}
	}()
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
			return Response{}, refused("the native guest handoff contains an unknown role")
		}
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			return Response{}, err
		}
		for _, fd := range fds {
			inputs = append(inputs, os.NewFile(uintptr(fd), "original-guest-native-input"))
		}
	}
	expectedInputs := 4
	if r.Op == "checkpoint" || r.Op == "checkpoint.reserve-v2" {
		expectedInputs = 7
		if r.Action == "open" {
			expectedInputs = 6
		}
	}
	if len(inputs) != expectedInputs {
		return Response{}, refused("native guest inspection needs the same socket, init, cgroup and executed VMM")
	}
	peerFD, err := unix.GetsockoptInt(int(inputs[0].Fd()), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return Response{}, refused("native peer handles need a Linux kernel with SO_PEERPIDFD support")
	}
	peer := os.NewFile(uintptr(peerFD), "original-guest-peer")
	retained := false
	defer func() {
		if !retained {
			peer.Close()
		}
	}()
	pid, err := nativePID(peer)
	if err != nil || !pidLive(peer) || !pidLive(inputs[1]) {
		return Response{}, refused("the original guest peer or namespace init ended")
	}
	initPID, err := nativePID(inputs[1])
	if err != nil {
		return Response{}, err
	}
	credential, err := unix.GetsockoptUcred(int(inputs[0].Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || int(credential.Uid) != allocation.SlotUID || int(credential.Gid) != allocation.SlotGID || int(credential.Pid) != pid {
		return Response{}, refused("the kernel guest peer differs from its original slot uid")
	}
	root, err := cgroupRoot()
	if err != nil {
		return Response{}, err
	}
	defer root.Close()
	cgroup, err := openCgroup(root, *allocation.Cgroup)
	if err != nil {
		return Response{}, err
	}
	defer cgroup.Close()
	left, err := cgroup.Stat()
	if err != nil {
		return Response{}, err
	}
	right, err := inputs[2].Stat()
	if err != nil || !os.SameFile(left, right) {
		return Response{}, refused("the guest inspection supplied another cgroup object")
	}
	if live, err := populated(cgroup); err != nil || !live {
		return Response{}, refused("the original guest cgroup is not populated")
	}
	proc := "/proc/" + strconv.Itoa(pid)
	policy, err := os.ReadFile("/proc/sys/fs/suid_dumpable")
	if err != nil || strings.TrimSpace(string(policy)) != "0" {
		return Response{}, refused("the native execute-only VMM has no non-dumpable host policy")
	}
	procInfo, err := os.Stat(proc)
	if err != nil || !owned(procInfo, 0) {
		return Response{}, refused("the kernel exposes the actual VMM as dumpable")
	}
	limits, err := os.ReadFile(proc + "/limits")
	if err != nil || len(limits) > RequestMax {
		return Response{}, refused("the actual VMM core limits cannot be checked")
	}
	coreZero := false
	for _, line := range strings.Split(string(limits), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 6 && strings.Join(parts[:4], " ") == "Max core file size" && parts[4] == "0" && parts[5] == "0" {
			coreZero = true
		}
		if len(parts) == 7 && strings.Join(parts[:4], " ") == "Max core file size" && parts[4] == "0" && parts[5] == "0" {
			coreZero = true
		}
	}
	if !coreZero {
		return Response{}, refused("the actual VMM has no inherited hard-zero core limit")
	}
	member, err := os.ReadFile(proc + "/cgroup")
	if err != nil {
		return Response{}, err
	}
	membership, err := unifiedPath(string(member))
	if err != nil || membership != allocation.Cgroup.Path {
		return Response{}, refused("the actual guest peer left its root-created cgroup")
	}
	status, err := os.ReadFile(proc + "/status")
	if err != nil || len(status) > RequestMax {
		return Response{}, refused("the native guest process facts cannot be captured")
	}
	want := map[string]string{"PPid:": strconv.Itoa(initPID), "CoreDumping:": "0", "CapInh:": "0000000000000000", "CapPrm:": "0000000000000000", "CapEff:": "0000000000000000", "CapBnd:": "0000000000000000", "CapAmb:": "0000000000000000", "NoNewPrivs:": "1", "Seccomp:": "2"}
	for _, line := range strings.Split(string(status), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			if expected, ok := want[parts[0]]; ok {
				if parts[1] != expected {
					return Response{}, refused("the native guest peer has another parent or keeps process capabilities")
				}
				delete(want, parts[0])
			}
		}
	}
	if len(want) != 0 {
		return Response{}, refused("the native guest peer facts are incomplete")
	}
	for _, namespace := range []string{"user", "pid"} {
		host, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			return Response{}, err
		}
		guest, err := os.Readlink(proc + "/ns/" + namespace)
		if err != nil || host == guest {
			return Response{}, refused("the native guest peer has no private user and PID namespaces")
		}
		initNS, err := os.Readlink("/proc/" + strconv.Itoa(initPID) + "/ns/" + namespace)
		if err != nil || initNS != guest {
			return Response{}, refused("the VMM left its original init namespaces")
		}
	}
	executable, err := os.Open(proc + "/exe")
	if err != nil {
		return Response{}, err
	}
	defer func() {
		if !retained {
			executable.Close()
		}
	}()
	actual, err := executable.Stat()
	if err != nil {
		return Response{}, err
	}
	expectedStat, err := inputs[3].Stat()
	if err != nil || !os.SameFile(actual, expectedStat) {
		return Response{}, refused("the actual guest peer executed another VMM object")
	}
	var execMount unix.Statfs_t
	if unix.Fstatfs(int(executable.Fd()), &execMount) != nil || execMount.Flags&unix.ST_RDONLY == 0 {
		return Response{}, refused("the executed VMM has no original read-only mount projection")
	}
	if err := checkSlotVMM(inputs[3], record.Digests[0]); err != nil {
		return Response{}, err
	}
	if !pidLive(peer) || !pidLive(inputs[1]) {
		return Response{}, refused("the original guest peer ended during its native inspection")
	}
	if r.Op == "checkpoint" || r.Op == "checkpoint.reserve-v2" {
		var directory *os.File
		if len(inputs) == 7 {
			directory = inputs[6]
		}
		if r.Op == "checkpoint.reserve-v2" {
			return b.reserveOriginalSlotV2(ctx, r, record, allocation, inputs[4], inputs[5], inputs[1], peer, directory)
		}
		return b.checkpointRunningV1(ctx, r, record, allocation, inputs[4], inputs[5], inputs[1], peer, directory)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return Response{}, err
	}
	defer self.Close()
	transfer := &nativeTransfer{files: []*os.File{peer, executable}}
	for _, file := range []*os.File{cgroup, inputs[1], self} {
		copy, err := dupFile(file)
		if err != nil {
			transfer.close()
			return Response{}, err
		}
		transfer.files = append(transfer.files, copy)
	}
	sender, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		transfer.close()
		return Response{}, err
	}
	transfer.files = append(transfer.files, os.NewFile(uintptr(sender), "root-native-inspector"))
	retained = true
	return Response{OK: true, native: transfer}, nil
}
