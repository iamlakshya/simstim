//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// CheckpointOperationV1 is bounded request DATA for fixed root operations.
// It grants no original Source, session operation, stop or execution right.
type CheckpointOperationV1 struct {
	Session    string
	Action     string
	Name       string
	Generation string
	KeepCount  int
	Metadata   []byte
	Needed     int64
	Safety     string
}

// CheckpointOperationOwnerV1 owns only one actual fixed root worker and its
// received output descriptors. It is not an outside checkpoint Source. The
// caller retains its genuine Source and SAME OP until Close positively joins
// this worker, including when RunCheckpointOperationV1 returns a late error.
type CheckpointOperationOwnerV1 struct {
	mu               sync.Mutex
	command          *exec.Cmd
	connection       *net.UnixConn
	worker           *os.File
	files            []*os.File
	result           Response
	checked, closing bool
	stop             func() bool
	cancelled        chan struct{}
	once             sync.Once
	closeErr         error
}

// RunCheckpointOperationV1 transports fixed actions over the exact seventeen
// already-held native operands. It creates no Source or stop permission from
// those operands. An opaque outside owner must separately prove its original
// live issuer, SAME OP and, for stopped mutations, actual whole-stop custody.
// The executing root helper independently checks its fixed native roles.
// On late errors a non-nil owner retains every materialised handle and actual
// worker. The caller MUST close/join it before releasing its original Source.
func RunCheckpointOperationV1(ctx context.Context, roles []*os.File, request CheckpointOperationV1) (*CheckpointOperationOwnerV1, error) {
	return runCheckpointOperationV2(ctx, roles, request, false)
}

// RunReservedCheckpointOperationV2 is resource-only transport for the distinct
// original reserved class. Its eighteen operands include the SAME actual EX
// lease. It creates no outside Source or stopped permission from a vector.
func RunReservedCheckpointOperationV2(ctx context.Context, roles []*os.File, request CheckpointOperationV1) (*CheckpointOperationOwnerV1, error) {
	return runCheckpointOperationV2(ctx, roles, request, true)
}

func runCheckpointOperationV2(ctx context.Context, roles []*os.File, request CheckpointOperationV1, reserved bool) (*CheckpointOperationOwnerV1, error) {
	roleCount := 17
	if reserved {
		roleCount = 18
		if request.Action == "cleanup" || request.Action == "destroy" {
			roleCount = 14
		}
		if request.Action == "abandon" {
			roleCount = 20
		}
		if request.Action == "prepare" {
			roleCount = 20
		} else if request.Action == "exchange" || request.Action == "rollback" || request.Action == "finalize" {
			roleCount = 21
		}
	} else if request.Action == "check" || request.Action == "discard" {
		roleCount = 19
	}
	if ctx == nil || len(roles) != roleCount {
		return nil, refused("the fixed checkpoint transport has no complete native operands")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for index, file := range roles {
		if file == nil && !(index == 16 && request.Action == "open") {
			return nil, refused("a fixed checkpoint native operand is absent")
		}
	}
	r := Request{Op: "checkpoint", Session: request.Session, Action: request.Action, Name: request.Name, Generation: request.Generation, KeepCount: request.KeepCount, MetaSize: int64(len(request.Metadata))}
	if reserved && (request.Action == "probe" || request.Action == "space" || request.Action == "list" || request.Action == "trim" || request.Action == "create" || request.Action == "select") {
		r.Op = "checkpoint.stopped-v2"
	} else if request.Action == "check" {
		r.Op = "checkpoint.read"
	} else if request.Action == "discard" && !reserved {
		r.Op = "checkpoint.discard-v2"
	} else if request.Action == "space" {
		if request.Name != "" || request.Generation != "" || request.KeepCount != 0 || len(request.Metadata) != 0 {
			return nil, refused("the checkpoint space operation has unrelated fields")
		}
		r.Op = "checkpoint.space"
		r.Needed = request.Needed
	} else if request.Action == "reserve" && !reserved {
		r.Op = "checkpoint.reserve-v2"
	} else if request.Action == "stop" && reserved {
		r.Op = "checkpoint.stop-v2"
	} else if request.Action == "cleanup" && reserved {
		r.Op = "reserved.cleanup-v2"
	} else if request.Action == "destroy" && reserved {
		r.Op = "reserved.destroy-v2"
	} else if request.Action == "abandon" && reserved {
		r.Op = "reserved.abandon-v2"
	} else if reserved && (request.Action == "prepare" || request.Action == "exchange" || request.Action == "rollback" || request.Action == "finalize") {
		r.Op = "checkpoint." + request.Action + "-v2"
		r.Safety = request.Safety
	} else if reserved && r.Op != "checkpoint.stopped-v2" {
		return nil, refused("the reserved checkpoint action is unknown")
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	launcher, err := fixedLauncher()
	if err != nil {
		return nil, err
	}
	defer launcher.Close()
	want, err := launcher.Stat()
	if err != nil {
		return nil, err
	}
	original, err := roles[11].Stat()
	if err != nil || !singleRegular(original, 0) || !os.SameFile(want, original) {
		return nil, refused("the original checkpoint root launcher changed")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "original-checkpoint-native-caller"), os.NewFile(uintptr(pair[1]), "original-checkpoint-root-operation")
	defer parent.Close()
	defer child.Close()
	if err := unix.SetsockoptInt(pair[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, err
	}
	connection, err := net.FileConn(parent)
	if err != nil {
		return nil, err
	}
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, refused("the fixed checkpoint transport has no native Unix channel")
	}
	command := exec.Command("/usr/bin/sudo", "-n", LauncherPath())
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	command.Stdin, command.Stdout, command.Stderr = child, child, io.Discard
	if err := command.Start(); err != nil {
		socket.Close()
		return nil, err
	}
	child.Close()
	owner := &CheckpointOperationOwnerV1{command: command, connection: socket, cancelled: make(chan struct{})}
	owner.stop = context.AfterFunc(ctx, func() { defer close(owner.cancelled); _ = socket.Close() })
	if err := socket.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return owner, err
	}
	wire := checkpointRequestV1{Op: r.Op, Session: r.Session, Action: r.Action, Name: r.Name, Generation: r.Generation, KeepCount: r.KeepCount, MetaSize: r.MetaSize}
	var envelope any = wire
	if request.Action == "space" && !reserved {
		envelope = struct {
			Op      string `json:"op"`
			Session string `json:"session"`
			Needed  int64  `json:"needed"`
		}{Op: r.Op, Session: r.Session, Needed: r.Needed}
	} else if request.Action == "reserve" || request.Action == "stop" || request.Action == "cleanup" || request.Action == "destroy" || request.Action == "abandon" {
		envelope = struct {
			Op      string `json:"op"`
			Session string `json:"session"`
		}{Op: r.Op, Session: r.Session}
	} else if reserved && r.Op != "checkpoint.stopped-v2" {
		envelope = struct {
			Op         string `json:"op"`
			Session    string `json:"session"`
			Name       string `json:"name"`
			Generation string `json:"generation"`
			Safety     string `json:"safety"`
			MetaSize   int64  `json:"meta_size"`
		}{r.Op, r.Session, r.Name, r.Generation, r.Safety, r.MetaSize}
	}
	if err := json.NewEncoder(socket).Encode(envelope); err != nil {
		return owner, err
	}
	initial, first, credentials, err := receiveNative(socket)
	if err != nil {
		owner.files = first
		return owner, err
	}
	if !initial.OK && len(first) == 0 {
		return owner, checkpointRemoteErrorV1(initial)
	}
	if !initial.OK || len(first) != 2 || credentials == nil || credentials.Uid != 0 {
		owner.files = first
		return owner, refused("the checkpoint worker did not issue its actual initial native creator")
	}
	pid, err := nativePID(first[1])
	if err != nil || pid != int(credentials.Pid) || !pidLive(first[1]) {
		owner.files = first
		return owner, refused("the checkpoint worker has another actual native sender")
	}
	executed, err := first[0].Stat()
	if err != nil || !singleRegular(executed, 0) || !os.SameFile(executed, original) {
		owner.files = first
		return owner, refused("the checkpoint worker executed another original root launcher")
	}
	owner.worker = first[1]
	first[0].Close()
	if _, err := socket.Write([]byte{1}); err != nil {
		return owner, err
	}
	var inputs []int
	if reserved {
		for _, file := range roles {
			inputs = append(inputs, int(file.Fd()))
		}
	} else {
		inputs = []int{int(roles[15].Fd()), int(roles[1].Fd()), int(roles[2].Fd()), int(roles[3].Fd()), int(roles[9].Fd()), int(roles[0].Fd())}
	}
	if !reserved && (request.Action == "check" || request.Action == "discard") {
		inputs = []int{int(roles[16].Fd()), int(roles[17].Fd()), int(roles[18].Fd())}
	} else if request.Action == "space" && !reserved {
		inputs = []int{int(roles[16].Fd()), int(roles[9].Fd())}
	} else if !reserved && request.Action != "open" {
		inputs = append(inputs, int(roles[16].Fd()))
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return owner, err
	}
	var sendErr error
	if err := raw.Write(func(fd uintptr) bool {
		sendErr = unix.Sendmsg(int(fd), []byte{1}, unix.UnixRights(inputs...), nil, unix.MSG_NOSIGNAL)
		return sendErr != unix.EAGAIN
	}); err != nil {
		return owner, err
	}
	if sendErr != nil {
		return owner, sendErr
	}
	if len(request.Metadata) > 0 {
		if _, err := socket.Write(request.Metadata); err != nil {
			return owner, err
		}
	}
	var created []*os.File
receiveCheckpointResultV2:
	response, files, credentials, err := receiveNative(socket)
	owner.result = response
	owner.files = append(append([]*os.File(nil), created...), files...)
	if err != nil {
		return owner, err
	}
	if !response.OK && len(files) == 0 {
		return owner, checkpointRemoteErrorV1(response)
	}
	expected := 2
	if request.Action == "open" || request.Action == "reserve" || request.Action == "prepare" {
		expected = 3
	}
	if request.Action == "select" {
		expected = 4
	}
	stage := request.Action == "create" && !reserved && response.CheckpointStage == "created"
	if stage {
		if len(created) != 0 {
			return owner, refused("the actual checkpoint creator repeated its native output loan")
		}
		expected = 4
	}
	if len(files) != expected || credentials == nil || credentials.Uid != 0 {
		return owner, refused("the root checkpoint operation returned another native role vector")
	}
	sender, err := nativePID(files[len(files)-1])
	if err != nil || sender != int(credentials.Pid) || !pidLive(files[len(files)-1]) {
		return owner, refused("the root checkpoint reply has another actual native sender")
	}
	initialPID, err := nativePID(owner.worker)
	if err != nil || sender != initialPID || !pidLive(owner.worker) {
		return owner, refused("another root worker completed the checkpoint transaction")
	}
	executed, err = files[len(files)-2].Stat()
	if err != nil || !singleRegular(executed, 0) || !os.SameFile(executed, original) {
		return owner, refused("the checkpoint producer executed another root launcher")
	}
	for _, file := range files[len(files)-2:] {
		file.Close()
	}
	owner.files = append(append([]*os.File(nil), created...), files[:len(files)-2]...)
	owner.checked = true
	if _, err := socket.Write([]byte{1}); err != nil {
		return owner, err
	}
	if stage {
		created = append([]*os.File(nil), owner.files...)
		goto receiveCheckpointResultV2
	}
	if !response.OK {
		return owner, checkpointRemoteErrorV1(response)
	}
	return owner, ctx.Err()
}

func checkpointRemoteErrorV1(response Response) error {
	if response.Code == "" || response.Error == "" || response.Hint == "" {
		return refused("the root checkpoint operation returned an incomplete refusal")
	}
	return &proto.Error{Code: response.Code, Message: response.Error, Hint: response.Hint}
}

// ResultV1 returns reply DATA only. It grants no output role or Source.
func (owner *CheckpointOperationOwnerV1) ResultV1() Response {
	if owner == nil {
		return Response{}
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	result := owner.result
	result.Checkpoints = slices.Clone(result.Checkpoints)
	result.Removed = slices.Clone(result.Removed)
	result.Slots = slices.Clone(result.Slots)
	result.CheckpointTransaction = append(json.RawMessage(nil), result.CheckpointTransaction...)
	return result
}

// WithFilesV1 borrows only outputs from a validated reply by the SAME actual
// root worker. They remain owned by this operation, even on a late error.
// A caller may duplicate them into its genuine private selected/output owner;
// neither a file vector nor the callback creates outside Source authority.
func (owner *CheckpointOperationOwnerV1) WithFilesV1(ctx context.Context, use func([]*os.File) error) error {
	if owner == nil || ctx == nil || use == nil {
		return refused("the root checkpoint operation has no output borrower")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closing || !owner.checked || ctx.Err() != nil {
		return refused("the root checkpoint outputs are unknown or their owner is closing")
	}
	return use(append([]*os.File(nil), owner.files...))
}

// Close joins this actual root worker before closing any materialised output
// or birth capability. A monitor/pipe error never counts as positive exit.
func (owner *CheckpointOperationOwnerV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.once.Do(func() {
		owner.mu.Lock()
		owner.closing = true
		owner.mu.Unlock()
		if owner.connection != nil {
			_ = owner.connection.Close()
		}
		if owner.command != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.command.Wait())
		}
		if owner.worker != nil {
			for !pidExited(owner.worker) {
				time.Sleep(10 * time.Millisecond)
			}
			owner.closeErr = errors.Join(owner.closeErr, owner.worker.Close())
		}
		if owner.stop != nil && !owner.stop() {
			<-owner.cancelled
		}
		for _, file := range owner.files {
			owner.closeErr = errors.Join(owner.closeErr, file.Close())
		}
	})
	return owner.closeErr
}
