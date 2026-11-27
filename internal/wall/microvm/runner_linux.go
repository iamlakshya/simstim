//go:build linux

package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
	"golang.org/x/sys/unix"
)

// RunVMHelper owns the VMM and its private Unix endpoints inside the canonical
// jail. It relays raw guest bytes and exposes only state/power-button control.
func RunVMHelper(in io.Reader) error {
	readonly, err := jail.CaptureVMMReadonlyControlStageV1()
	if err != nil {
		return err
	}
	if readonly != nil {
		return runReadonlyVMControlV1(readonly)
	}
	if err := helperNoDump(); err != nil {
		return err
	}
	var plan runnerPlan
	if err := decodeHelper(in, &plan); err != nil {
		return err
	}
	if plan.Version != 1 && plan.Version != 2 {
		return fmt.Errorf("unknown microvm runner version")
	}
	if err := validateVMSpec(plan.Spec); err != nil {
		return err
	}
	kind, err := backendForSpec(plan.Spec)
	if err != nil {
		return err
	}
	if err := helperUID(plan.UIDMode); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	raw, err := inheritedConnection(9)
	if err != nil {
		return err
	}
	defer raw.Close()
	controlConn, err := inheritedConnection(10)
	if err != nil {
		return err
	}
	defer controlConn.Close()
	server := newRunnerServer(controlConn)
	files := make([]*os.File, 6)
	for i := range files {
		files[i] = os.NewFile(uintptr(3+i), "runtime-capability")
		defer files[i].Close()
		unix.CloseOnExec(int(files[i].Fd()))
	}
	mode, err := captureRunnerExecModeV1(ctx, plan.Version, files[0])
	if err != nil {
		return err
	}
	defer mode.Close()
	root, err := cachefs.OpenPrivateRoot(vmDataDir(plan.Spec), false)
	if err != nil {
		return err
	}
	defer root.Close()
	disk := os.NewFile(11, "original-session-disk")
	defer disk.Close()
	unix.CloseOnExec(int(disk.Fd()))
	if err := checkRunnerDiskV1(root, disk, plan.Spec.DiskMax); err != nil {
		return err
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	for _, path := range []string{api, vsock} {
		if err := removeOwnSocket(path); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/9", "chv-exec")
	command.ExtraFiles = []*os.File{files[0], files[1], files[2], files[3], files[4], disk, files[5]}
	if mode != nil {
		command.ExtraFiles = append(command.ExtraFiles, mode.lease, mode.writer)
	} // final private10/11
	command.Env = []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	createdFD := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &createdFD}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	mask := unix.Umask(0o177)
	err = command.Start()
	unix.Umask(mask)
	if err != nil {
		return fmt.Errorf("start owned VMM execution helper: %w", err)
	}
	if createdFD < 0 {
		command.Process.Kill()
		command.Wait()
		return fmt.Errorf("the VMM child has no native creation handle")
	}
	owner := &ownedVMMEndpoint{fd: createdFD, pid: command.Process.Pid, uid: uint32(os.Geteuid())}
	defer func() { cancel(); _ = owner.close() }()
	// The child now holds its exact mapped copies. The trusted runner needs
	// no program, runtime-read or disk capability while it relays/control-dials.
	for _, file := range files {
		file.Close()
	}
	disk.Close()
	var planErr error
	if mode != nil {
		planErr = server.sendNativeModeExecChild(plan.Spec.ID, kind.name(), owner)
		if planErr == nil {
			planErr = writeFinalExecPlanV2(stdin, plan)
		}
	} else {
		planErr = json.NewEncoder(stdin).Encode(plan)
	}
	if planErr != nil {
		stdin.Close()
		command.Process.Kill()
		command.Wait()
		return fmt.Errorf("send VMM execution plan: %w", planErr)
	}
	if mode != nil {
		if err := server.receiveModeContinueV1(ctx, plan.BootUntil, stdin); err != nil {
			stdin.Close()
			command.Process.Kill()
			command.Wait()
			return fmt.Errorf("receive original direct VMM continuation: %w", err)
		}
		// Actual last-helper adoption and parent Arm were required for the
		// original host continuation. This ancestor can never exec another VMM.
		if err := mode.Close(); err != nil {
			command.Process.Kill()
			command.Wait()
			return err
		}
	} else {
		stdin.Close()
	}
	ended := make(chan runnerExit, 1)
	go func() { err := command.Wait(); ended <- ownedVMMExit(command, err) }()
	connectCtx, cancelConnect := context.WithDeadline(ctx, plan.BootUntil)
	defer cancelConnect()
	exitObserved := make(chan runnerExit, 1)
	go func() {
		select {
		case exit := <-ended:
			exitObserved <- exit
			cancelConnect()
		case <-ctx.Done():
			cancelConnect()
		}
	}()
	guest, err := connectGuest(connectCtx, vsock, owner.dial, pauseVMConnect)
	if err != nil {
		cancel()
		exit := waitOwnedExit(exitObserved, ended)
		_ = server.send(runnerMessage{Kind: "exit", Exit: &exit})
		return fmt.Errorf("connect owned guest vsock: %w", err)
	}
	defer guest.Close()
	var fcControl *fc.Client
	if kind.isFirecracker() {
		apiConnection, err := owner.connectAPI(connectCtx, api)
		if err == nil {
			fcControl, err = fc.NewClient(apiConnection)
		}
		if err != nil {
			cancel()
			waitOwnedExit(exitObserved, ended)
			return fmt.Errorf("connect original Firecracker control endpoint: %w", err)
		}
		defer fcControl.Close()
	}
	if err := server.sendNativeReady(plan.Spec.ID, kind.name(), owner, guest); err != nil {
		cancel()
		waitOwnedExit(exitObserved, ended)
		return err
	}
	go server.serve(ctx, api, owner, fcControl)
	go func() { _, _ = io.Copy(guest, raw); guest.Close() }()
	go func() { _, _ = io.Copy(raw, guest); raw.Close() }()
	exit := waitOwnedExit(exitObserved, ended)
	_ = server.send(runnerMessage{Kind: "exit", Exit: &exit})
	return nil
}

func checkRunnerDiskV1(root *os.Root, disk *os.File, bytes int64) error {
	info, err := cachefs.ValidateFile(root, SessionDiskName, disk, 0o600)
	if err != nil {
		return fmt.Errorf("the VMM runner did not inherit its original session disk: %w", err)
	}
	if info.Size() != bytes {
		return fmt.Errorf("the inherited VM session disk changed size")
	}
	return nil
}

func decodeHelper(in io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(in, helperLimit+1))
	if err != nil {
		return fmt.Errorf("read closed VMM helper plan: %w", err)
	}
	if len(data) > helperLimit {
		return fmt.Errorf("VMM helper plan exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode closed VMM helper plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("VMM helper plan has trailing content")
	}
	return nil
}
func inheritedConnection(fd int) (net.Conn, error) {
	file := os.NewFile(uintptr(fd), "private-VMM-channel")
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, fmt.Errorf("use private VMM channel: %w", err)
	}
	return conn, nil
}
func ownedVMMExit(command *exec.Cmd, err error) runnerExit {
	exit := runnerExit{Code: 0}
	if err == nil {
		return exit
	}
	exit.Code = -1
	exit.Error = err.Error()
	var ended *exec.ExitError
	if errors.As(err, &ended) {
		exit.Code = ended.ExitCode()
		if status, ok := ended.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			exit.Signal = status.Signal().String()
		}
	}
	return exit
}
func waitOwnedExit(observed, ended <-chan runnerExit) runnerExit {
	select {
	case exit := <-observed:
		return exit
	case exit := <-ended:
		return exit
	}
}

func removeOwnSocket(path string) error {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect old VMM socket: %w", err)
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || before.Mode()&os.ModeSocket == 0 || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("old VMM socket path is not an owned socket")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("old VMM socket changed identity")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove old owned VMM socket: %w", err)
	}
	return nil
}

type runnerServer struct {
	readonlyDisk *readonlyControlDiskV2 // only the captured original C-created B
	conn         net.Conn
	encoder      *json.Encoder
	mu           sync.Mutex
}

func newRunnerServer(conn net.Conn) *runnerServer {
	return &runnerServer{conn: conn, encoder: json.NewEncoder(conn)}
}
func (s *runnerServer) send(message runnerMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readonlyDisk != nil && message.Kind == "reply" {
		return s.sendReadonlyDiskReplyV2(message)
	}
	if err := s.encoder.Encode(message); err != nil {
		return fmt.Errorf("send private VMM reply: %w", err)
	}
	return nil
}

func closeRuntimeDescriptors(keep map[int]bool) error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("inspect VMM inherited descriptors: %w", err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 3 {
			continue
		}
		if keep[fd] {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
				return fmt.Errorf("retain VMM boot descriptor: %w", err)
			}
		} else {
			unix.CloseOnExec(fd)
		}
	}
	return nil
}
