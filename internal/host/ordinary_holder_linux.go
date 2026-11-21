//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

type ownedOrdinaryHolderV1 struct {
	native        *sessionFileLease
	generation    string
	socketMu      sync.Mutex
	endpoints     []*ordinaryEndpointV1
	socketsClosed bool
}

func acquireOrdinaryHolderV1(id string) (*ownedOrdinaryHolderV1, func(), error) {
	if err := secretmem.Protect(); err != nil {
		return nil, nil, err
	}
	var lease *sessionFileLease
	var err error
	if os.Getenv(holderLockFDEnv) == "3" {
		_ = os.Unsetenv(holderLockFDEnv)
		unix.CloseOnExec(3)
		file := os.NewFile(3, "original-ordinary-holder")
		// The launcher may have exited. A retained flock's owning PID is
		// accounting; only this exact still-locked OFD is adopted here.
		if err := checkOrdinaryHolderFlockV1(file); err != nil {
			return nil, nil, errors.Join(err, file.Close())
		}
		lease, err = adoptOrdinaryHolderFileV1(context.Background(), id, file)
	} else {
		lease, err = openOwnedSessionFileLease(context.Background(), id, holderLockFile, 500*time.Millisecond)
	}
	if err != nil {
		return nil, nil, err
	}
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, nil, errors.Join(err, lease.Close())
	}
	owner := &ownedOrdinaryHolderV1{native: lease, generation: hex.EncodeToString(generation[:])}
	return owner, func() {
		for _, endpoint := range owner.endpoints {
			_ = endpoint.root.Close()
		}
		_ = lease.Close()
	}, nil
}

func prepareHolderMainV1(cmd *exec.Cmd) *holderMainV1 {
	main := &holderMainV1{cmd: cmd, pidfd: -1}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.PidFD = &main.pidfd
	return main
}
func signalHolderMainV1(main *holderMainV1, signal syscall.Signal) error {
	if main.pidfd >= 0 {
		return unix.PidfdSendSignal(main.pidfd, unix.Signal(signal), nil, 0)
	}
	// Older kernels keep the ordinary UI working. Go's original Process
	// serialises Signal against Wait; it never signals a numeric group.
	// This route cannot issue the native stopped source below.
	return main.cmd.Process.Signal(signal)
}

func readHolderFirstFrameV1(connection net.Conn) (proto.Frame, io.Reader, *StartupLineV1, error) {
	if _, ok := connection.(*net.UnixConn); !ok {
		frame, err := proto.ReadFrom(connection)
		return frame, connection, nil, err
	}
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		return proto.Frame{}, connection, nil, err
	}
	frame, err := line.ReadFrame()
	return frame, line.OrdinaryReader(), line, err
}

// This purpose has its own reader from the first byte and cannot attach,
// execute a command or lend any VM/slot resource rights.
func (h *Holder) serveOrdinaryStopV1(c *hclient, line *StartupLineV1, hook bool) {
	h.serveOrdinaryControlV2(c, line, hook, false)
}

func (h *Holder) serveOrdinaryControlV2(c *hclient, line *StartupLineV1, hook, reversible bool) {
	if line == nil {
		return
	}
	defer line.Close()
	if hook || h.ordinary == nil || h.ordinary.native == nil || h.guest != nil || h.vmTerm != nil || h.main == nil || h.main.pidfd < 0 || !ordinaryManifestV1(h.m) {
		return
	}
	if err := h.acceptOrdinaryControlHelloV3(line, reversible); err != nil {
		return
	}
	if err := line.nativePeer.Check(); err != nil {
		return
	}
	if access, refused := h.accessOf(c.conn, false, "bridge"); refused != nil || access != accessFull {
		return
	}
	if err := h.ordinary.native.check(context.Background()); err != nil {
		return
	}
	role := ordinaryHolderRoleV1
	if reversible {
		role = ordinaryPurgeHolderRoleV2
	}
	if err := line.WriteNativeHelloV1(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: role}); err != nil {
		return
	}
	state, err := h.readOrdinaryStopRequestV1(line, 1, ordinaryStopInspectV1)
	if err != nil || state.Generation != "" {
		return
	}
	state.Generation = h.ordinary.generation
	joinedFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return
	}
	joined := os.NewFile(uintptr(joinedFD), "actual-ordinary-main-pty-vt-join")
	defer joined.Close()
	if err := h.writeOrdinaryStateV1(line, 1, state); err != nil {
		return
	}
	holder, err := h.duplicateOrdinaryHolderV1()
	if err != nil {
		return
	}
	defer holder.Close()
	if err := sendOrdinaryPrivateFileV1(line, 1, state, holder); err != nil {
		return
	}
	descriptor, err := unix.FcntlInt(uintptr(h.main.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return
	}
	main := os.NewFile(uintptr(descriptor), "original-ordinary-main-pidfd")
	defer main.Close()
	if err := sendOrdinaryPrivateFileV1(line, 1, state, main); err != nil {
		return
	}
	if err := sendOrdinaryPrivateFileV1(line, 1, state, joined); err != nil {
		return
	}
	if reversible {
		h.serveOrdinaryPurgeRequestsV2(line, state, joined)
		return
	}
	commit, err := h.readOrdinaryStopRequestV1(line, 2, ordinaryStopCommitV1)
	if err != nil || commit != state {
		return
	}
	h.commitOrdinaryStopV1(line, 2, state, joined)
}

func (h *Holder) commitOrdinaryStopV1(line *StartupLineV1, id uint64, state ordinaryStopStateV1, joined *os.File) {
	// Once committed, cancellation, parent death or transport failure cannot
	// release the original holder before its actual main/PTY/VT have joined.
	h.stopMu.Lock()
	if err := h.joinOwnedMainV1(); err != nil {
		h.stopMu.Unlock()
		return
	}
	// Flush the final manifest before ACK. closeSockets removes only this
	// producer's original endpoints; the outside owner retains their source.
	h.mu.Lock()
	copy := h.copyForSave()
	h.mu.Unlock()
	if err := h.save(copy, true); err != nil {
		h.stopMu.Unlock()
		return
	}
	if err := h.closeOriginalOrdinarySocketsV1(); err != nil {
		h.stopMu.Unlock()
		return
	}
	var completion [8]byte
	binary.NativeEndian.PutUint64(completion[:], 1)
	if n, err := joined.Write(completion[:]); err != nil || n != len(completion) {
		h.stopMu.Unlock()
		return
	}
	err := h.writeOrdinaryStateV1(line, id, state)
	h.stopMu.Unlock()
	// Even a lost ACK completes only this already-committed bounded stop.
	_ = err
	go h.stop("killed by request", false)
}

func (h *Holder) readOrdinaryStopRequestV1(line *StartupLineV1, id uint64, operation string) (ordinaryStopStateV1, error) {
	_ = line.SetDeadline(time.Now().Add(10 * time.Second))
	state, actual, err := h.readOrdinaryControlRequestV2(line, id)
	if err == nil && actual != operation {
		err = sessionLeaseError("the original ordinary request changed its closed purpose")
	}
	return state, err
}

func (h *Holder) readOrdinaryControlRequestV2(line *StartupLineV1, id uint64) (ordinaryStopStateV1, string, error) {
	frame, err := line.ReadFrame()
	if err != nil {
		return ordinaryStopStateV1{}, "", err
	}
	if err := line.RejectAncillary(); err != nil {
		return ordinaryStopStateV1{}, "", err
	}
	sender, err := line.currentSender()
	if err != nil {
		return ordinaryStopStateV1{}, "", err
	}
	if err := line.nativePeer.CheckSender(sender); err != nil {
		return ordinaryStopStateV1{}, "", err
	}
	var request proto.Request
	var state ordinaryStopStateV1
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != id || !api.ExactJSONFields(request.Args, state) || json.Unmarshal(request.Args, &state) != nil || state.Version != 1 || state.Primary != h.id || !supply.IsHex64(state.Nonce) {
		return state, "", sessionLeaseError("the ordinary request has no exact original continuation")
	}
	return state, request.Op, nil
}
func (h *Holder) writeOrdinaryStateV1(line *StartupLineV1, id uint64, state ordinaryStopStateV1) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return line.WriteFrame(proto.TResponse, proto.Response{ID: id, OK: true, Result: raw})
}
func (h *Holder) duplicateOrdinaryHolderV1() (*os.File, error) {
	lease := h.ordinary.native
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if err := lease.checkLocked(context.Background()); err != nil {
		return nil, err
	}
	if err := checkOrdinaryHolderFlockV1(lease.file); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(lease.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "same-original-ordinary-holder-ofd"), nil
}
func sendOrdinaryPrivateFileV1(line *StartupLineV1, id uint64, state ordinaryStopStateV1, file *os.File) error {
	result, err := json.Marshal(state)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(proto.Response{ID: id, OK: true, Result: result})
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	return proto.SendUnixFrameV1(line.conn.(*net.UnixConn), proto.TResponse, raw, []*os.File{file})
}

// Managed ordinary holders use the same original outside host-root closure
// as the native launcher. Manager ambient HOME/XDG values cannot select roots.
func managedOrdinaryEnvironmentV1(unit string) []string { return supervisorLaunchEnv(unit) }
