package jail

import (
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const stageGvisorOuterNativeV1 = "native-gvisor-init-v1"

// nativeGvisorOuterStageV1 can only consume the fixed C role's fresh socket.
// C created that socket after its actual first map, group clear and ND0. Init
// reasserts ND0 after exec before this entry reads any control or private data.
func nativeGvisorOuterStageV1() int {
	runtime.LockOSThread()
	// First operation after Init actual ND0: consume only the fixed public
	// prelude, then move/refill both low control roles without any opens.
	slotCount, controlFD, gateFD, preludeErr := consumeGvisorSlotPreludeV1()
	if preludeErr != nil {
		return stageFailed(preludeErr)
	}
	if os.Getpid() != 1 || os.Getppid() != 0 {
		return stageFailed(gvisorHandoffFailureV2("the native outer helper is not its fresh PID1"))
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	groups, groupErr := unix.Getgroups()
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	if err != nil || dumpable != 0 || groupErr != nil || len(groups) != 0 || ruid != 0 || euid != 0 || suid != 0 || rgid != 0 || egid != 0 || sgid != 0 {
		return stageFailed(errors.Join(err, groupErr, gvisorHandoffFailureV2("the actual native outer identity or ND0 changed after fixed exec")))
	}
	permission, err := os.ReadFile("/proc/self/setgroups")
	if err != nil || string(permission) != "deny\n" {
		return stageFailed(errors.Join(err, gvisorHandoffFailureV2("the first native group deny is missing")))
	}
	// The exact C gate/control objects are already high; every future low
	// target remains a harmless null placeholder while Go allocates its poller.
	gate := os.NewFile(uintptr(gateFD), "original-gvisor-C-death-gate")
	defer func() { _ = gate.Close() }()
	if err = checkGvisorOuterGateV1(gate); err != nil {
		_ = unix.Close(controlFD)
		return stageFailed(err)
	}
	original := os.NewFile(uintptr(controlFD), "native-gvisor-postmap-control")
	connValue, err := net.FileConn(original)
	closeErr := original.Close()
	if err != nil || closeErr != nil {
		return stageFailed(errors.Join(err, closeErr))
	}
	conn, ok := connValue.(*net.UnixConn)
	if !ok {
		_ = connValue.Close()
		return stageFailed(gvisorHandoffFailureV2("the native outer control is not Unix"))
	}
	defer conn.Close()
	if err = proto.EnableUnixCredentialsV1(conn); err != nil {
		return stageFailed(err)
	}
	peer, err := gvisorPeerV2(conn)
	// SO_PEERCRED describes the socketpair's actual creator A; it is not an
	// advertised outside PID and grants no private receive authority by itself.
	if err != nil || peer.Pid != 1 || peer.Uid != 0 || peer.Gid != 0 {
		return stageFailed(errors.Join(err, gvisorHandoffFailureV2("the post-map socket did not originate in this actual A task")))
	}
	own, err := openGvisorProcV2("/proc/self")
	if err != nil {
		return stageFailed(err)
	}
	defer own.Close()
	ownRaw, err := readGvisorAtV2(own, "stat")
	if err != nil {
		return stageFailed(err)
	}
	ownTask, err := parseGvisorTaskV2(ownRaw)
	if err != nil || ownTask.parent <= 0 {
		return stageFailed(errors.Join(err, gvisorHandoffFailureV2("the native outer has no original C creator")))
	}
	creator, err := openGvisorProcV2("/proc/" + strconv.Itoa(ownTask.parent))
	if err != nil {
		return stageFailed(err)
	}
	defer creator.Close()
	creatorRaw, err := readGvisorAtV2(creator, "stat")
	if err != nil {
		return stageFailed(err)
	}
	creatorTask, err := parseGvisorTaskV2(creatorRaw)
	if err != nil || creatorTask.parent <= 0 {
		return stageFailed(errors.Join(err, gvisorHandoffFailureV2("the original C creator has no outside owner")))
	}
	outside, err := openGvisorProcV2("/proc/" + strconv.Itoa(creatorTask.parent))
	if err != nil {
		return stageFailed(err)
	}
	defer outside.Close()
	helper, err := os.Open(self)
	if err != nil {
		return stageFailed(err)
	}
	socketFD, err := gvisorConnectionFDV2(conn)
	if err != nil {
		_ = helper.Close()
		return stageFailed(err)
	}
	err = sendGvisorV2(conn, gvisorMessageV2{Phase: "outer-mapped", SocketFD: socketFD, SlotCount: slotCount}, []*os.File{helper})
	err = errors.Join(err, helper.Close())
	if err != nil {
		return stageFailed(err)
	}
	sender := unix.Ucred{Pid: 0, Uid: 0, Gid: 0}
	proof, received, err := receiveGvisorV2(conn, sender, "outer-ancestry", 3)
	if err != nil {
		return stageFailed(err)
	}
	err = verifyGvisorOuterAncestryV1([]*os.File{own, creator, outside}, received, socketFD, proof.SocketFD)
	err = errors.Join(err, closeGvisorProofV1(received), own.Close(), creator.Close(), outside.Close())
	if err != nil {
		return stageFailed(err)
	}
	if err = checkGvisorOuterGateV1(gate); err != nil {
		return stageFailed(err)
	}
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "outer-proved"}, nil); err != nil {
		return stageFailed(err)
	}
	c, err := receiveGvisorPlanControlV2(conn, sender, stageInit, &gate, slotCount, true)
	if err != nil {
		return stageFailed(err)
	}
	if err = checkGvisorOuterGateV1(gate); err != nil {
		return stageFailed(err)
	}
	c.gvisorOuterNative, c.gvisorOuterGate = true, gate
	return runInit(c, os.Environ())
}

func checkGvisorOuterGateV1(gate *os.File) error {
	if gate == nil {
		return gvisorHandoffFailureV2("the original C creator death gate is missing")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(gate.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.Join(err, gvisorHandoffFailureV2("the C creator gate has another native role"))
	}
	events := []unix.PollFd{{Fd: int32(gate.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(events, 0)
	if err != nil || n != 0 {
		return errors.Join(err, gvisorHandoffFailureV2("the original C creator ended or changed its death gate"))
	}
	return nil
}

func closeGvisorProofV1(files []*os.File) error {
	var result error
	for _, file := range files {
		result = errors.Join(result, file.Close())
	}
	return result
}

func verifyGvisorOuterAncestryV1(local, received []*os.File, socketFD, sentSocketFD int) error {
	if len(local) != 3 || len(received) != 3 || socketFD != sentSocketFD {
		return gvisorHandoffFailureV2("the original outer proof has different native roles")
	}
	tasks := make([]gvisorTaskV2, 3)
	var device uint64
	for index := range local {
		a, err := local[index].Stat()
		if err != nil {
			return err
		}
		b, err := received[index].Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(a, b) {
			return gvisorHandoffFailureV2("a native outer ancestry task was substituted")
		}
		var fs unix.Statfs_t
		var st unix.Stat_t
		if err := errors.Join(unix.Fstatfs(int(local[index].Fd()), &fs), unix.Fstat(int(local[index].Fd()), &st)); err != nil {
			return err
		}
		if fs.Type != unix.PROC_SUPER_MAGIC || index > 0 && st.Dev != device {
			return gvisorHandoffFailureV2("the original outer ancestry crosses process superblocks")
		}
		device = st.Dev
		raw, err := readGvisorAtV2(received[index], "stat")
		if err != nil {
			return err
		}
		tasks[index], err = parseGvisorTaskV2(raw)
		if err != nil {
			return err
		}
	}
	if tasks[0].parent != tasks[1].pid || tasks[1].parent != tasks[2].pid {
		return gvisorHandoffFailureV2("the kernel task chain is not A to original C to outside creator")
	}
	var socket unix.Stat_t
	if err := unix.Fstat(socketFD, &socket); err != nil || socket.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(err, gvisorHandoffFailureV2("the outer task lost its actual fresh control socket"))
	}
	buffer := make([]byte, 256)
	n, err := unix.Readlinkat(int(local[0].Fd()), "fd/"+strconv.Itoa(socketFD), buffer)
	if err != nil || string(buffer[:max(n, 0)]) != "socket:["+strconv.FormatUint(socket.Ino, 10)+"]" {
		return errors.Join(err, gvisorHandoffFailureV2("the native outer proof does not own this fresh socket inode"))
	}
	for index := range local {
		raw, err := readGvisorAtV2(local[index], "stat")
		if err != nil {
			return err
		}
		current, err := parseGvisorTaskV2(raw)
		if err != nil || current != tasks[index] {
			return errors.Join(err, gvisorHandoffFailureV2("the original outer native birth or ancestry changed"))
		}
	}
	return nil
}
