//go:build linux

package jail

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// Serialized layout is correlation only. The original Cmd channel and native
// creation chain must adopt every stage before that stage can release a child.
type vmmExecSetupV1 struct {
	ReadonlyInit  bool   `json:"readonly_init,omitempty"`
	ReadonlyNonce string `json:"readonly_nonce,omitempty"`
	Version       int    `json:"version"`
	LeaseFD       int    `json:"lease_fd"`
	WriterFD      int    `json:"writer_fd"`
	ControlFD     int    `json:"control_fd"`
}

const vmmRunnerMarkerV1 = "--original-vmm-native-stage-v1"
const (
	stageOuterVMMV1 = "outer-vmm-mode-v1"
	stageInitVMMV1  = "init-vmm-mode-v1"
	stageExecVMMV1  = "exec-vmm-mode-v1"
)

func vmmFileRulesStageV1(stage string, c *setup) string {
	if c.VMMExecV1 == nil {
		return stage
	}
	switch stage {
	case stageOuter, stageOuterFilesV1:
		return stageOuterVMMV1
	case stageInit, stageInitFilesV1:
		return stageInitVMMV1
	case stageExec, stageExecFilesV1:
		return stageExecVMMV1
	}
	return stage
}
func checkVMMFileRulesStageV1(stage string, c *setup) (string, error) {
	normal := ""
	switch stage {
	case stageOuterVMMV1:
		normal = stageOuterFilesV1
	case stageInitVMMV1:
		normal = stageInitFilesV1
	case stageExecVMMV1:
		normal = stageExecFilesV1
	}
	if normal == "" {
		if c.VMMExecV1 != nil || c.ReadonlyVMMInitV1 != nil {
			return "", vmmModeFailureV1("a generic stage cannot consume a private VMM mode source")
		}
		return stage, nil
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return "", err
	}
	return normal, nil
}
func checkVMMLayoutV1(c *setup) error {
	if err := checkVMMLayoutMetadataV1(c); err != nil {
		return err
	}
	return checkVMMStageCapabilitiesV1(c.VMMExecV1)
}
func checkVMMLayoutMetadataV1(c *setup) error {
	if c == nil || c.VMMExecV1 == nil {
		return vmmModeFailureV1("the closed VMM stage is missing its native layout")
	}
	value := c.VMMExecV1
	base := 5 + c.Files + len(c.Binds) + len(pinnedRules(c.Rules))
	if value.Version != 2 || value.LeaseFD != base || value.WriterFD != base+1 || value.ControlFD != base+2 || c.Files != 9 || c.InitLeaseV1Version != 0 || c.ControllerPublicV1 != nil || c.PublicationLeaseV1 != nil || c.GvisorV1Version != 0 || !c.Userns || c.Net != NetNone || c.Seccomp != ProfileVMM || c.ExecFD != 8 || len(c.Argv) != 2 || c.Argv[0] != "/proc/self/fd/8" || c.Argv[1] != "chv-run" {
		return vmmModeFailureV1("the private VMM stage has another fixed purpose or native layout")
	}
	if value.ReadonlyInit {
		if c.Sub == nil || len(value.ReadonlyNonce) != 32 || !validReadonlyNonceV1(value.ReadonlyNonce) {
			return vmmModeFailureV1("the read-only init lost its actual two-map source correlation")
		}
		if c.ReadonlyVMMInitV1 == nil {
			return vmmModeFailureV1("the read-only init lost its original selected plan")
		}
	} else if value.ReadonlyNonce != "" || c.ReadonlyVMMInitV1 != nil {
		return vmmModeFailureV1("a writable phase cannot consume read-only init metadata")
	}
	return nil
}
func checkVMMStageCapabilitiesV1(value *vmmExecSetupV1) error {
	if err := checkVMMReadonlyRegularV1(3, 0o555); err != nil {
		return err
	}
	return checkVMMPrivateCapabilitiesV1(value)
}
func checkVMMReadonlyRegularV1(fd int, mode uint32) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != mode || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return vmmModeFailureV1("the private VMM readonly capability changed its exact native role")
	}
	return nil
}
func checkVMMPrivateCapabilitiesV1(value *vmmExecSetupV1) error {
	if err := checkVMMReadonlyRegularV1(value.LeaseFD, 0o600); err != nil {
		return err
	}
	var pipe unix.Stat_t
	var fs unix.Statfs_t
	if err := errors.Join(unix.Fstat(value.WriterFD, &pipe), unix.Fstatfs(value.WriterFD, &fs)); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(uintptr(value.WriterFD), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if pipe.Mode&unix.S_IFMT != unix.S_IFIFO || fs.Type != 0x50495045 || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return vmmModeFailureV1("the original VMM final sentinel changed native role")
	}
	kind, err := unix.GetsockoptInt(value.ControlFD, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return err
	}
	if kind != unix.SOCK_SEQPACKET {
		return vmmModeFailureV1("the original VMM creation source is not a native packet channel")
	}
	return nil
}
func appendVMMInheritedV1(c *setup, extra []*os.File) []*os.File {
	if c.VMMExecV1 == nil {
		return extra
	}
	for _, fd := range []int{c.VMMExecV1.LeaseFD, c.VMMExecV1.WriterFD, c.VMMExecV1.ControlFD} {
		extra = append(extra, os.NewFile(uintptr(fd), "original-vmm-private-inheritance"))
	}
	return extra
}
func addPrivateVMMRulesV1(c *setup, rs *ruleset) error {
	if c.VMMExecV1 == nil {
		return nil
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	return rs.add(c.VMMExecV1.LeaseFD, Read)
}
func vmmStageConnectionV1(value *vmmExecSetupV1) (*net.UnixConn, error) {
	fd, err := unix.FcntlInt(uintptr(value.ControlFD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-vmm-stage-channel")
	conn, err := net.FileConn(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		if conn != nil {
			conn.Close()
		}
		return nil, errors.Join(err, closeErr)
	}
	local, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, vmmModeFailureV1("the stage lost its actual native creation channel")
	}
	return local, nil
}
func sendVMMStageV1(c *setup, kind, stage byte, child *os.File) error {
	if c.VMMExecV1 == nil {
		return nil
	}
	if kind == vmmModeNoFutureV1 {
		// Only this fixed creator terminal path has already stripped generic
		// source capabilities. Its original private source must still be intact.
		if err := checkVMMLayoutMetadataV1(c); err != nil {
			return err
		}
		if err := checkVMMPrivateCapabilitiesV1(c.VMMExecV1); err != nil {
			return err
		}
	} else if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	conn, err := vmmStageConnectionV1(c.VMMExecV1)
	if err != nil {
		return err
	}
	defer conn.Close()
	var rights []*os.File
	if kind == vmmModeEnterV1 {
		for _, path := range []string{self, "/proc/self/ns/user", "/proc/self/ns/pid", "/proc/self/ns/mnt"} {
			file, err := os.Open(path)
			if err != nil {
				closeFiles(rights)
				return err
			}
			rights = append(rights, file)
		}
		defer closeFiles(rights)
	} else if kind == vmmModeCreateV1 {
		rights = []*os.File{child}
	}
	message := vmmNativeMessageV1{kind: kind, stage: stage, localPID: uint32(os.Getpid())}
	if err := proto.SendUnixControlV1(conn, message.bytes(), rights); err != nil {
		return err
	}
	var ack [1]byte
	n, err := conn.Read(ack[:])
	if err != nil || n != 1 || ack[0] != 0xa6 {
		return errors.Join(err, vmmModeFailureV1("the original host did not adopt this native VMM stage"))
	}
	return nil
}
func enterVMMStageV1(c *setup, stage string) error {
	if c.VMMExecV1 == nil {
		return nil
	}
	var role byte
	switch stage {
	case stageOuter:
		role = vmmStageOuterV1
	case stageInit:
		role = vmmStageInitV1
	case stageExec:
		role = vmmStageExecV1
	default:
		return vmmModeFailureV1("the mode source entered another helper stage")
	}
	return sendVMMStageV1(c, vmmModeEnterV1, role, nil)
}
func reportVMMCreationV1(c *setup, stage string, child *os.Process, createdFD int) error {
	if c.VMMExecV1 == nil {
		return nil
	}
	if child == nil || createdFD < 0 {
		return vmmModeFailureV1("the trusted VMM creator omitted its actual creation pidfd")
	}
	process := os.NewFile(uintptr(createdFD), "original-vmm-next-creation")
	defer process.Close()
	pid, _, err := vmmPIDFDIdentityV1(process)
	if err != nil || pid != child.Pid {
		return errors.Join(err, vmmModeFailureV1("the actual child differs from its atomic creation handle"))
	}
	role := vmmStageExecV1
	if stage == stageOuter {
		role = vmmStageInitV1
	} else if stage != stageInit {
		return vmmModeFailureV1("another helper tried to create a VMM stage")
	}
	return sendVMMStageV1(c, vmmModeCreateV1, role, process)
}

// This hook is called only after successful setup release, on the fixed
// creator path whose remaining code is signal forwarding and native reaping.
// Its child already owns the SAME original OFD and sentinel. It is never a
// Cmd.Start receipt and never restores the inode or ends the parent's fence.
func finishVMMCreatorV1(c *setup, creatorStage string, extra []*os.File) error {
	if c.VMMExecV1 == nil {
		return nil
	}
	stage := vmmStageInitV1
	if creatorStage == stageOuter {
		stage = vmmStageOuterV1
	} else if creatorStage != stageInit {
		return vmmModeFailureV1("another stage requested a creator terminal boundary")
	}
	if err := sendVMMStageV1(c, vmmModeNoFutureV1, stage, nil); err != nil {
		return err
	}
	if len(extra) < 3 {
		return vmmModeFailureV1("the original creator lost its inherited private wrappers")
	}
	roles := extra[len(extra)-3:]
	for index, fd := range []int{c.VMMExecV1.LeaseFD, c.VMMExecV1.WriterFD, c.VMMExecV1.ControlFD} {
		if roles[index] == nil || int(roles[index].Fd()) != fd {
			return vmmModeFailureV1("the creator's private wrappers changed their original native slots")
		}
	}
	return closeInitInheritedV1(roles)
}

// VMMExecLeaseStageV1 is captured only by the actual re-executed trusted
// runner after its original host adopts that same creation actor and code.
// It can lend private child roles; it cannot Arm or restore an inode.
type VMMExecLeaseStageV1 struct {
	mu                     sync.Mutex
	program                os.FileInfo
	lease, writer, control *os.File
	taken, closed          bool
}

var capturedVMMRunnerV1 struct {
	sync.Mutex
	stage *VMMExecLeaseStageV1
}

// captureVMMRunnerStageV1 is called by Init before ordinary command dispatch.
// A marker alone does nothing: this exact executable must complete the native
// host handshake over the retained original channel before Capture can return.
func captureVMMRunnerStageV1() error {
	if len(os.Args) != 3 || os.Args[1] != "chv-run" || os.Args[2] != vmmRunnerMarkerV1 {
		return nil
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	value := &vmmExecSetupV1{Version: 2, LeaseFD: 12, WriterFD: 13, ControlFD: 14}
	if err := checkVMMStageCapabilitiesV1(value); err != nil {
		return err
	}
	conn, err := vmmStageConnectionV1(value)
	if err != nil {
		return err
	}
	defer conn.Close()
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
	if err := proto.SendUnixControlV1(conn, (vmmNativeMessageV1{vmmModeEnterV1, vmmStageRunnerV1, uint32(os.Getpid())}).bytes(), rights); err != nil {
		return err
	}
	var ack [1]byte
	n, err := conn.Read(ack[:])
	if err != nil || n != 1 || ack[0] != 0xa6 {
		return errors.Join(err, vmmModeFailureV1("the original host did not adopt its re-executed VMM runner"))
	}
	info, err := os.Stat("/proc/self/fd/3")
	if err != nil {
		return err
	}
	stage := &VMMExecLeaseStageV1{program: info, lease: os.NewFile(12, "original-vmm-stage-lease"), writer: os.NewFile(13, "original-vmm-stage-sentinel"), control: os.NewFile(14, "original-vmm-stage-creation")}
	capturedVMMRunnerV1.Lock()
	defer capturedVMMRunnerV1.Unlock()
	if capturedVMMRunnerV1.stage != nil {
		return vmmModeFailureV1("the original VMM runner stage was already captured")
	}
	capturedVMMRunnerV1.stage = stage
	os.Args = os.Args[:2]
	return nil
}

// CaptureVMMExecLeaseStageV1 consumes the one native stage captured by Init.
func CaptureVMMExecLeaseStageV1() (*VMMExecLeaseStageV1, error) {
	capturedVMMRunnerV1.Lock()
	defer capturedVMMRunnerV1.Unlock()
	stage := capturedVMMRunnerV1.stage
	capturedVMMRunnerV1.stage = nil
	return stage, nil
}

// RetainForNextHelperV1 lends the original OFD and sentinel once to the
// actual next helper. The captured runner keeps its own copies until continuation.
func (stage *VMMExecLeaseStageV1) RetainForNextHelperV1(ctx context.Context, program *os.File) (*os.File, *os.File, error) {
	if stage == nil || program == nil {
		return nil, nil, vmmModeFailureV1("the original captured VMM runner stage is missing")
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if stage.closed || stage.taken {
		return nil, nil, vmmModeFailureV1("the captured VMM runner stage has already been consumed")
	}
	info, err := program.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, stage.program) || info.Mode() != 0o555 {
		return nil, nil, vmmModeFailureV1("the actual next helper received another projected VMM")
	}
	lease, err := captureFile(stage.lease, "original VMM sidecar OFD", "Keep its original native runner stage.")
	if err != nil {
		return nil, nil, err
	}
	writer, err := captureFile(stage.writer, "original VMM final sentinel", "Keep its original native runner stage.")
	if err != nil {
		lease.Close()
		return nil, nil, err
	}
	stage.taken = true
	return lease, writer, nil
}

// Close drops this trusted runner's private copies at its no-future boundary.
func (stage *VMMExecLeaseStageV1) Close() error {
	if stage == nil {
		return nil
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.closed {
		return nil
	}
	stage.closed = true
	return errors.Join(stage.lease.Close(), stage.writer.Close(), stage.control.Close())
}

// closeVMMInitInheritedV1 closes all ordinary setup capabilities before the
// child is released. Only the exact private roles survive to the creator's
// fixed no-future hook; that hook closes them after its original host ACK.
func closeVMMInitInheritedV1(c *setup, extra []*os.File) error {
	if c.VMMExecV1 == nil {
		return closeInitInheritedV1(extra)
	}
	var result error
	for _, file := range extra {
		if file == nil {
			continue
		}
		fd := int(file.Fd())
		if fd == c.VMMExecV1.LeaseFD || fd == c.VMMExecV1.WriterFD || fd == c.VMMExecV1.ControlFD {
			continue
		}
		result = errors.Join(result, file.Close())
	}
	return result
}

func validReadonlyNonceV1(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}
