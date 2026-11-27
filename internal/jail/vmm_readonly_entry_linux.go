//go:build linux

package jail

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// Go init runs on the startup thread. Keep the original native leader for the
// closed VMM namespace-init and single-exec control roles before main can move.
func init() {
	initRole := len(os.Args) == 3 && os.Args[0] == argv0 && os.Args[1] == stageInitVMMV1 && os.Getpid() == 1
	controlRole := len(os.Args) == 4 && os.Args[1] == "chv-run" && os.Args[2] == vmmReadonlyControlMarkerV1
	if !initRole && !controlRole {
		return
	}
	runtime.LockOSThread()
	if unix.Gettid() != os.Getpid() {
		os.Exit(stageFailed(vmmModeFailureV1("the original VMM entry lost its native startup leader")))
	}
}

func readonlyUntilV1(c *setup) (time.Time, error) {
	if c == nil || c.ReadonlyVMMInitV1 == nil {
		return time.Time{}, vmmModeFailureV1("the read-only init lost its original plan")
	}
	var data struct {
		BootUntil time.Time `json:"boot_until"`
	}
	if err := json.Unmarshal(c.ReadonlyVMMInitV1.Runtime, &data); err != nil {
		return time.Time{}, err
	}
	if data.BootUntil.IsZero() || !time.Now().Before(data.BootUntil) {
		return time.Time{}, vmmModeFailureV1("the original VMM boot deadline ended")
	}
	return data.BootUntil, nil
}

func readonlyConnectionV1(fd int) (*net.UnixConn, error) {
	file := os.NewFile(uintptr(fd), "original-readonly-control-stream")
	value, err := net.FileConn(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		if value != nil {
			value.Close()
		}
		return nil, errors.Join(err, closeErr)
	}
	conn, ok := value.(*net.UnixConn)
	if !ok {
		value.Close()
		return nil, vmmModeFailureV1("the original read-only control is not Unix")
	}
	if err := proto.EnableUnixCredentialsV1(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func receiveReadonlyParentV1(ctx context.Context, conn *net.UnixConn, until time.Time, marker string, roles int) (files []*os.File, returned error) {
	if err := conn.SetReadDeadline(until); err != nil {
		return nil, err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		conn.SetReadDeadline(time.Time{})
	}()
	var data [16]byte
	offset := 0
	var first proto.UnixCredentialsV1
	peerSeen := false
	// Bound each physical receive. Stream fragmentation cannot lose or shift
	// the FIRST native witness/rights; the packet channel remains one frame.
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	kind := 0
	var native error
	if err := raw.Control(func(fd uintptr) { kind, native = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE) }); err != nil || native != nil {
		return nil, errors.Join(err, native)
	}
	if kind != unix.SOCK_STREAM && kind != unix.SOCK_SEQPACKET {
		return nil, vmmModeFailureV1("the original parent frame has another native socket type")
	}
	for offset < len(data) {
		n, control, err := proto.ReceiveUnixControlV1(conn, data[offset:], roles)
		if err == nil {
			err = control.CheckReceivedV1(conn)
		}
		if err == nil && (n <= 0 || control.Credentials == nil || control.Credentials.UID != 0 || control.Credentials.GID != 0) {
			err = vmmModeFailureV1("the read-only stage received another original-parent frame")
		}
		if err == nil && offset == 0 {
			first = *control.Credentials
			peerSeen = true
			if len(control.Files) != roles {
				err = vmmModeFailureV1("the original parent frame omitted its fixed native roles")
			} else {
				files, err = control.DetachFilesV1()
			}
		} else if err == nil && (len(control.Files) != 0 || !peerSeen || first != *control.Credentials) {
			err = vmmModeFailureV1("the original parent changed sender/rights inside its frame")
		}
		closeErr := control.Close()
		if err != nil || closeErr != nil {
			closeFiles(files)
			return nil, errors.Join(err, closeErr)
		}
		offset += n
		if kind == unix.SOCK_SEQPACKET && offset != len(data) {
			closeFiles(files)
			return nil, vmmModeFailureV1("the original parent packet has another fixed length")
		}
	}
	if string(data[:]) != marker {
		closeFiles(files)
		return nil, vmmModeFailureV1("the original parent frame has another closed phase")
	}
	if err := ctx.Err(); err != nil {
		closeFiles(files)
		return nil, err
	}
	return files, nil
}

func sendReadonlyNativeV1(c *setup, kind, stage byte, rights []*os.File) error {
	if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	conn, err := vmmStageConnectionV1(c.VMMExecV1)
	if err != nil {
		return err
	}
	defer conn.Close()
	return proto.SendUnixControlV1(conn, (vmmNativeMessageV1{kind: kind, stage: stage, localPID: uint32(os.Getpid())}).bytes(), rights)
}

func approveVMMReadonlyParentV1(c *setup) error {
	until, err := readonlyUntilV1(c)
	if err != nil {
		return err
	}
	// The EXISTING mapping pipe's second byte means A consumed its terminal
	// ACK and no longer reads the shared native endpoint. It grants no exec.
	if err := awaitReadonlyOuterV1(c.SyncFD, until); err != nil {
		return err
	}
	if err := checkVMMReadonlyControlParentV1(c); err != nil {
		return err
	}
	conn, err := vmmStageConnectionV1(c.VMMExecV1)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := proto.EnableUnixCredentialsV1(conn); err != nil {
		return err
	}
	if err := proto.SendUnixControlV1(conn, (vmmNativeMessageV1{kind: vmmReadonlyControlParentV1, stage: vmmStageInitV1, localPID: uint32(os.Getpid())}).bytes(), nil); err != nil {
		return err
	}
	_, err = receiveReadonlyParentV1(context.Background(), conn, until, vmmReadonlyInitApprovalV1, 0)
	return err
}

func awaitReadonlyOuterV1(fd int, until time.Time) error {
	for {
		left := time.Until(until)
		if left <= 0 {
			return vmmModeFailureV1("the original outer did not complete its native terminal boundary")
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, int(min(left.Milliseconds()+1, 100)))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		var data [1]byte
		n, err = unix.Read(fd, data[:])
		if err != nil {
			return err
		}
		if n != 1 || data[0] != 0x72 {
			return vmmModeFailureV1("the original outer ended before its native read-only boundary")
		}
		return nil
	}
}

// VMMReadonlyControlStageV1 exists only after the original S accepted this
// actual C-created B's code/ns/current sender and lent C's original pidfd.
// Its plan projection is data; no row/marker/descriptor setter constructs it.
type VMMReadonlyControlStageV1 struct {
	setup                   *setup
	control                 *net.UnixConn
	parent                  *os.File
	parentPID               int
	inputs                  []*os.File
	lease, writer, creation *os.File
	closeMu                 sync.Mutex
	terminal                bool
}

var capturedReadonlyControlV1 struct {
	sync.Mutex
	stage *VMMReadonlyControlStageV1
}

func captureVMMReadonlyControlStageV1() error {
	if len(os.Args) != 4 || os.Args[1] != "chv-run" || os.Args[2] != vmmReadonlyControlMarkerV1 {
		return nil
	}
	if unix.Gettid() != os.Getpid() {
		return vmmModeFailureV1("the original control helper lost its native startup thread")
	}
	c, err := readSetup(os.Args[3])
	if err != nil {
		return err
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	if err := c.ReadonlyVMMInitV1.checkV1(c); err != nil {
		return err
	}
	if err := verifyReadonlyCollapsedV1(c); err != nil {
		return err
	}
	until, err := readonlyUntilV1(c)
	if err != nil {
		return err
	}
	conn, err := readonlyConnectionV1(10)
	if err != nil {
		return err
	}
	stage := &VMMReadonlyControlStageV1{setup: c, control: conn, lease: os.NewFile(uintptr(c.VMMExecV1.LeaseFD), "original-readonly-control-lease"), writer: os.NewFile(uintptr(c.VMMExecV1.WriterFD), "original-readonly-control-sentinel"), creation: os.NewFile(uintptr(c.VMMExecV1.ControlFD), "original-readonly-control-source")}
	for fd := 3; fd <= 8; fd++ {
		stage.inputs = append(stage.inputs, os.NewFile(uintptr(fd), "original-readonly-control-input"))
	}
	stage.inputs = append(stage.inputs, os.NewFile(11, "original-readonly-control-parent"))
	var rights []*os.File
	for _, path := range []string{self, "/proc/self/ns/user", "/proc/self/ns/pid", "/proc/self/ns/mnt"} {
		file, err := os.Open(path)
		if err != nil {
			closeFiles(rights)
			return err
		}
		rights = append(rights, file)
	}
	defer closeFiles(rights)
	if err := sendReadonlyNativeV1(c, vmmReadonlyControlEnterV1, vmmStageRunnerV1, rights); err != nil {
		return err
	}
	files, err := receiveReadonlyParentV1(context.Background(), conn, until, vmmReadonlyControlApprovalV1, 1)
	if err != nil {
		return err
	}
	stage.parent = files[0]
	pid, namespace, err := vmmPIDFDIdentityV1(stage.parent)
	if err != nil || pid != 1 || len(namespace) == 0 || namespace[len(namespace)-1] != 1 {
		return errors.Join(err, vmmModeFailureV1("the control helper did not receive its original namespace-init handle"))
	}
	stage.parentPID = pid
	if err := enforceReadonlyControlV1(c); err != nil {
		return err
	}
	capturedReadonlyControlV1.Lock()
	defer capturedReadonlyControlV1.Unlock()
	if capturedReadonlyControlV1.stage != nil {
		return vmmModeFailureV1("the original read-only control stage was already captured")
	}
	capturedReadonlyControlV1.stage = stage
	os.Args = os.Args[:2]
	return nil
}

func verifyReadonlyCollapsedV1(c *setup) error {
	uid, gid := readonlyExecutionIDsV1(c)
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	if ruid != uid || euid != uid || suid != uid || rgid != gid || egid != gid || sgid != gid {
		return vmmModeFailureV1("the original control helper did not collapse to its mapped subordinate identity")
	}
	if err := checkVMMReadonlyCurrentIDsV1(uint64(uid), [4]uint64{uint64(gid), uint64(gid), uint64(gid), uint64(gid)}); err != nil {
		return err
	}
	if err := verifyInitPrivateStateV1(); err != nil {
		return err
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || nnp != 1 {
		return errors.Join(err, vmmModeFailureV1("the original control helper lacks native NNP"))
	}
	return nil
}

func CaptureVMMReadonlyControlStageV1() (*VMMReadonlyControlStageV1, error) {
	capturedReadonlyControlV1.Lock()
	defer capturedReadonlyControlV1.Unlock()
	stage := capturedReadonlyControlV1.stage
	capturedReadonlyControlV1.stage = nil
	return stage, nil
}
func (stage *VMMReadonlyControlStageV1) PlanV1() ([]byte, error) {
	if stage == nil || stage.setup == nil {
		return nil, vmmModeFailureV1("the original read-only control source is missing")
	}
	return append([]byte(nil), stage.setup.ReadonlyVMMInitV1.Runtime...), nil
}
func (stage *VMMReadonlyControlStageV1) ControlV1() (*net.UnixConn, error) {
	if stage == nil || stage.control == nil {
		return nil, vmmModeFailureV1("the original read-only control stream is missing")
	}
	return stage.control, nil
}
func (stage *VMMReadonlyControlStageV1) RetainInitV1() (*os.File, int, error) {
	if stage == nil || stage.parent == nil {
		return nil, 0, vmmModeFailureV1("the original namespace-init handle is missing")
	}
	raw, err := stage.parent.SyscallConn()
	if err != nil {
		return nil, 0, err
	}
	var ended bool
	var native error
	err = raw.Control(func(fd uintptr) { ended, native = ownedPIDFDReady(int(fd), 0) })
	if err != nil || native != nil || ended {
		return nil, 0, errors.Join(err, native, vmmModeFailureV1("the original namespace init ended before its retained loan"))
	}
	pid := stage.parentPID
	if pid != 1 {
		return nil, 0, vmmModeFailureV1("the original namespace-init identity was not captured")
	}

	fd, err := unix.FcntlInt(stage.parent.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, 0, err
	}
	return os.NewFile(uintptr(fd), "original-readonly-init-loan"), pid, nil
}

func (stage *VMMReadonlyControlStageV1) ReleaseNoExecV1() error {
	if stage == nil {
		return vmmModeFailureV1("the original control no-exec source is missing")
	}
	stage.closeMu.Lock()
	defer stage.closeMu.Unlock()
	if stage.terminal {
		return vmmModeFailureV1("the original control no-exec boundary was already consumed")
	}
	if err := verifyReadonlyCollapsedV1(stage.setup); err != nil {
		return err
	}
	if err := sendReadonlyNativeV1(stage.setup, vmmReadonlyControlTerminalV1, vmmStageRunnerV1, nil); err != nil {
		return err
	}
	stage.terminal = true
	for _, file := range append(stage.inputs, stage.lease, stage.writer, stage.creation) {
		if file != nil {
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
	stage.inputs = nil
	stage.lease, stage.writer, stage.creation = nil, nil, nil
	return nil
}

func (stage *VMMReadonlyControlStageV1) WaitExecV1(ctx context.Context, until time.Time) error {
	if stage == nil || !stage.terminal {
		return vmmModeFailureV1("the original control helper has no fixed no-exec boundary")
	}
	_, err := receiveReadonlyParentV1(ctx, stage.control, until, vmmReadonlyExecContinueV1, 0)
	return err
}

func (stage *VMMReadonlyControlStageV1) Close() error {
	if stage == nil {
		return nil
	}
	stage.closeMu.Lock()
	defer stage.closeMu.Unlock()
	var returned error
	for _, file := range append(stage.inputs, stage.parent, stage.lease, stage.writer, stage.creation) {
		if file != nil {
			err := file.Close()
			if !errors.Is(err, os.ErrClosed) {
				returned = errors.Join(returned, err)
			}
		}
	}
	if stage.control != nil {
		returned = errors.Join(returned, stage.control.Close())
	}
	return returned
}
