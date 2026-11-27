package jail

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const stageGvisorMapV2 = "gvisor-self-map-v3"
const stageGvisorControllerMapV1 = "gvisor-controller-self-map-v1"
const maxGvisorSlotsV2 = 1024

// gvisorMessageV2 is control data, never an execution or namespace grant. Only
// this exact creation child and the held original descriptor vector can act.
type gvisorMessageV2 struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	SlotCount int    `json:"slot_count,omitempty"`
	SocketFD  int    `json:"socket_fd,omitempty"`
	Setup     *setup `json:"setup,omitempty"`
	Slots     []int  `json:"slots,omitempty"`
}

type gvisorReaderV2 struct {
	conn  *net.UnixConn
	peer  unix.Ucred
	files []*os.File
}

func (r *gvisorReaderV2) Read(data []byte) (int, error) {
	n, control, err := proto.ReceiveUnixControlV1(r.conn, data, 8)
	if err != nil {
		return n, err
	}
	if control.Credentials == nil || control.Credentials.PID != int(r.peer.Pid) || control.Credentials.UID != r.peer.Uid || control.Credentials.GID != r.peer.Gid {
		return n, errors.Join(control.Close(), gvisorHandoffFailureV2("the current native sender changed"))
	}
	r.files = append(r.files, control.Files...)
	if len(r.files) > 8 {
		return n, gvisorHandoffFailureV2("one bootstrap frame exceeded its exact native descriptor bound")
	}
	return n, nil
}

func receiveGvisorV2(conn *net.UnixConn, peer unix.Ucred, phase string, count int) (gvisorMessageV2, []*os.File, error) {
	reader := &gvisorReaderV2{conn: conn, peer: peer}
	frame, err := proto.ReadFrom(reader)
	var message gvisorMessageV2
	if err == nil && frame.Type != proto.TRequest {
		err = gvisorHandoffFailureV2("the bootstrap frame has another role")
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(frame.Payload))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&message)
		var extra any
		if err == nil && !errors.Is(decoder.Decode(&extra), io.EOF) {
			err = gvisorHandoffFailureV2("the bootstrap frame has trailing content")
		}
	}
	if err == nil && (message.Version != 2 || message.Phase != phase || len(reader.files) != count) {
		err = gvisorHandoffFailureV2("the bootstrap phase or descriptor vector changed")
	}
	if err != nil {
		closeFiles(reader.files)
		return message, nil, err
	}
	return message, reader.files, nil
}

func sendGvisorV2(conn *net.UnixConn, message gvisorMessageV2, files []*os.File) error {
	message.Version = 2
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return proto.SendUnixFrameV1(conn, proto.TRequest, data, files)
}

func gvisorPeerV2(conn *net.UnixConn) (unix.Ucred, error) {
	var peer *unix.Ucred
	raw, err := conn.SyscallConn()
	if err != nil {
		return unix.Ucred{}, err
	}
	var nativeErr error
	err = raw.Control(func(fd uintptr) { peer, nativeErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err = errors.Join(err, nativeErr); err != nil {
		return unix.Ucred{}, err
	}
	return *peer, nil
}

func gvisorConnectionFDV2(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd := -1
	err = raw.Control(func(value uintptr) { fd = int(value) })
	return fd, err
}

// startGvisorExecV2 is the sole trusted phase split. A already has a safe
// mounted root, ND0, no capabilities and its fixed seccomp filters. No generic
// jail may defer Landlock, and no parsed command runs while A is deferred.
func startGvisorExecV2(c *setup, env []string, rs *ruleset) (pid int, failureErr error) {
	if c.ControllerPublicV1 == nil || c.gvisorReport == nil || !c.gvisorOuterNative || c.GvisorV1Version != 1 || !c.Userns || c.Sub != nil || c.Net != NetNone || c.Seccomp != profileGvisorV1 {
		return 0, gvisorHandoffFailureV2("the original closed bootstrap purpose is absent")
	}
	if err := checkGvisorOuterGateV1(c.gvisorOuterGate); err != nil {
		return 0, err
	}
	if err := verifyInitPrivateStateV1(); err != nil {
		return 0, err
	}
	groups, err := unix.Getgroups()
	permission, permissionErr := os.ReadFile("/proc/self/setgroups")
	if err != nil || len(groups) != 0 || permissionErr != nil || string(permission) != "deny\n" {
		return 0, errors.Join(err, permissionErr, gvisorHandoffFailureV2("the original outer producer did not clear groups before the first deny"))
	}
	if rs.fd >= 0 {
		defer func() {
			if rs.fd >= 0 {
				_ = unix.Close(rs.fd)
				rs.fd = -1
			}
		}()
	}
	extra := inherited(c, false)
	if len(extra)+3 > maxGvisorSlotsV2 {
		return 0, gvisorHandoffFailureV2("the original bootstrap vector exceeds its fixed bound")
	}
	defer func() { failureErr = errors.Join(failureErr, closeInitInheritedV1(extra)) }()
	name := "@jail-gvisor-" + rand.Text()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	raw, err := listener.SyscallConn()
	if err != nil {
		return 0, err
	}
	var nativeErr error
	err = raw.Control(func(fd uintptr) { nativeErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) })
	if err = errors.Join(err, nativeErr); err != nil {
		return 0, err
	}
	deadline := time.Now().Add(10 * time.Second)
	if err = listener.SetDeadline(deadline); err != nil {
		return 0, err
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer null.Close()
	placeholders := make([]*os.File, len(extra)+3)
	for i := range placeholders {
		placeholders[i] = null
	}
	pidfd := -1
	attr := gvisorExecNamespacesV1()
	attr.PidFD = &pidfd
	mapStage := stageGvisorControllerMapV1
	child, err := os.StartProcess(self, []string{argv0, mapStage, name, strconv.Itoa(len(placeholders))}, &os.ProcAttr{Env: env, Files: placeholders, Sys: attr})
	if err != nil {
		return 0, err
	}
	var creation *os.File
	if pidfd >= 0 {
		creation = os.NewFile(uintptr(pidfd), "original-nested-gvisor-creation")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// The original Process and creation pidfd stay here until real exit. No
		// timeout, EOF or recycled PID can release a living child or its sources.
		if pidfd >= 0 {
			failureErr = errors.Join(failureErr, unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0))
			for {
				ready, waitErr := ownedPIDFDReady(pidfd, 100)
				if waitErr != nil {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if ready {
					break
				}
			}
		} else {
			_ = child.Kill()
		}
		_, waitErr := child.Wait()
		failureErr = errors.Join(failureErr, waitErr)
		if creation != nil {
			failureErr = errors.Join(failureErr, creation.Close())
		}
	}()
	if pidfd < 0 {
		return 0, gvisorHandoffFailureV2("the creation child has no original pidfd")
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err = conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	if err = proto.EnableUnixCredentialsV1(conn); err != nil {
		return 0, err
	}
	peer, err := gvisorPeerV2(conn)
	if err != nil || int(peer.Pid) != child.Pid || peer.Uid != 0 || peer.Gid != 0 {
		return 0, errors.Join(err, gvisorHandoffFailureV2("the connector is not the exact creation child"))
	}
	hello, _, err := receiveGvisorV2(conn, peer, "mapped", 0)
	if err != nil || hello.SlotCount != len(placeholders) || hello.SocketFD < 3 || hello.Setup != nil || len(hello.Slots) != 0 {
		return 0, errors.Join(err, gvisorHandoffFailureV2("the self-mapped child hello is invalid"))
	}
	if ready, err := ownedPIDFDReady(pidfd, 0); err != nil || ready {
		return 0, errors.Join(err, gvisorHandoffFailureV2("the creation child ended before its native proof"))
	}
	// A's original filesystem layer must succeed before ANY proof/private/setup release.
	// Its IPC/signal scope was enforced before this child was created; a new
	// post-fork scope would make A and C siblings and deny original-child kill.
	if kernelABI() >= 6 {
		if err := c.gvisorCommonScope.checkOriginal(c, kernelABI()); err != nil {
			return 0, err
		}
	}
	if err = rs.enforce(); err != nil {
		rs.fd = -1
		return 0, err
	}
	rs.fd = -1
	if c.gvisorCommonScope != nil {
		if err := c.gvisorCommonScope.Close(); err != nil {
			return 0, err
		}
		c.gvisorCommonScope = nil
	}
	// Transfer the SAME original creation source before a child proof, plan or
	// private descriptor can be released. The outside owner keeps its own
	// native lifetime even if this helper later fails or dies.
	if err = reportGvisorNestedCreationV1(c, child, creation); err != nil {
		return 0, err
	}
	childProc, err := openGvisorProcV2("/proc/" + strconv.Itoa(child.Pid))
	if err != nil {
		return 0, err
	}
	defer childProc.Close()
	parentProc, err := openGvisorProcV2("/proc/self")
	if err != nil {
		return 0, err
	}
	defer parentProc.Close()
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "ancestry", SocketFD: hello.SocketFD}, []*os.File{childProc, parentProc}); err != nil {
		return 0, err
	}
	if _, _, err = receiveGvisorV2(conn, peer, "proved", 0); err != nil {
		return 0, err
	}
	next := *c
	next.StatusFD, next.SyncFD, next.Sub = 0, 0, nil
	slots := []int{0, 1, 2}
	sources := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	if c.ControllerPublicV1 != nil {
		sources = []*os.File{null, null, null}
	}
	for i, file := range extra {
		if file != nil {
			slots = append(slots, 3+i)
			sources = append(sources, file)
		}
	}
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "plan", Setup: &next, Slots: slots}, nil); err != nil {
		return 0, err
	}
	for offset := 0; offset < len(sources); offset += 8 {
		end := min(offset+8, len(sources))
		if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "files", Slots: slots[offset:end]}, sources[offset:end]); err != nil {
			return 0, err
		}
	}
	// These are the original wrappers, not fd-number reconstructions. Release
	// is impossible while A still holds any raw Files/bind/rule capability.
	if err = closeInitInheritedV1(extra); err != nil {
		extra = nil
		return 0, err
	}
	extra = nil
	if ready, err := ownedPIDFDReady(pidfd, 0); err != nil || ready {
		return 0, errors.Join(err, gvisorHandoffFailureV2("the original child ended before setup release"))
	}
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "release"}, nil); err != nil {
		return 0, err
	}
	c.gvisorCreation, c.gvisorCreationChild = creation, child
	committed = true
	return child.Pid, nil
}

func runGvisorMapV2(address string, slotCount int) error {
	return gvisorHandoffFailureV2("the legacy generic mapped runtime purpose is retired")
}
func runGvisorControllerMapV1(address string, slotCount int) error {
	return runGvisorMappedRoleV1(address, slotCount)
}
func runGvisorMappedRoleV1(address string, slotCount int) error {
	runtime.LockOSThread()
	if err := verifyGvisorNullVectorV1(slotCount, false); err != nil {
		return err
	}
	if len(address) < 16 || len(address) > 100 || address[:13] != "@jail-gvisor-" {
		return gvisorHandoffFailureV2("the closed bootstrap rendezvous is invalid")
	}
	// No setup pipe, socket or private right exists at this point. Every
	// inherited stdio/future slot is /dev/null; Init has already asserted ND0.
	if err := mapGvisorSelfV2(); err != nil {
		return err
	}
	if err := prepareGvisorBootstrapV1(); err != nil {
		return err
	}
	ownA, err := openGvisorProcV2("/proc/self")
	if err != nil {
		return err
	}
	defer ownA.Close()
	parentA, err := openGvisorProcV2("/proc/1")
	if err != nil {
		return err
	}
	defer parentA.Close()
	if err = mountGvisorProcV2(); err != nil {
		return err
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err = proto.EnableUnixCredentialsV1(conn); err != nil {
		return err
	}
	if err = confineGvisorBootstrapV2(ownA, parentA); err != nil {
		return err
	}
	peer, err := gvisorPeerV2(conn)
	// PID0 is expected for an ancestor sender in C, but confers no authority.
	// Only the native own-task/parent ancestry proof below can authorise input.
	if err != nil || peer.Pid != 0 || peer.Uid != 0 || peer.Gid != 0 {
		return errors.Join(err, gvisorHandoffFailureV2("the namespace rendezvous has another native identity"))
	}
	socketFD, err := gvisorConnectionFDV2(conn)
	if err != nil {
		return err
	}
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "mapped", SocketFD: socketFD, SlotCount: slotCount}, nil); err != nil {
		return err
	}
	proof, proofFiles, err := receiveGvisorV2(conn, peer, "ancestry", 2)
	if err != nil {
		return err
	}
	err = verifyGvisorAncestryV2(ownA, parentA, proofFiles[0], proofFiles[1], socketFD, proof.SocketFD)
	err = errors.Join(err, proofFiles[0].Close(), proofFiles[1].Close(), ownA.Close(), parentA.Close())
	if err != nil {
		return err
	}
	if err = sendGvisorV2(conn, gvisorMessageV2{Phase: "proved"}, nil); err != nil {
		return err
	}
	c, err := receiveGvisorPlanV2(conn, peer, stageExec, nil, slotCount)
	if err != nil {
		return err
	}
	if c.ControllerPublicV1 == nil {
		return gvisorHandoffFailureV2("the actual closed controller map lost its original public purpose")
	}
	return execMappedGvisorControllerV1(*c.ControllerPublicV1)
}

func equalGvisorSlotsV2(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func gvisorHandoffFailureV2(message string) error {
	return failure("the gvisor native bootstrap refused: "+message, "Retain the original jail owner and inspect this host's namespace confinement.")
}
