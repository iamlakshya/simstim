//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

const auxiliaryLoanOperationV1 = "operation_loan_v1"

type auxiliaryLoanV1 struct {
	Version int    `json:"v"`
	Primary string `json:"primary"`
	Nonce   string `json:"nonce"`
	Token   string `json:"token"`
	Need    string `json:"need"`
	Runtime string `json:"runtime"`
}

// AuxiliaryBootCandidateV1 is a live closed-helper candidate. Its public
// challenge is not authority. Only the actual outside creator's native OP and
// holder OFD pair, matching new native cap/membership and protected reservation
// can produce an AuxiliaryBootV1 from it.
type AuxiliaryBootCandidateV1 struct {
	primary string
	line    *StartupLineV1
	peer    *nativeStartupPeerV1
	manager *nativeUserManagerV1
	nonce   string
	used    bool
	mu      sync.Mutex
}

// AuxiliaryBootV1 belongs only to this NEW native closed auxiliary helper.
// It never edits ordinary Run/Life or issues a client value/ACK permission.
type AuxiliaryBootV1 struct {
	disk                   *os.File
	diskRef                cachefs.Reference
	diskParent             *os.Root
	diskParentRef          cachefs.Reference
	ctx                    context.Context
	cancel                 context.CancelFunc
	candidate              *AuxiliaryBootCandidateV1
	operation              *SessionOperation
	holder                 *OwnedSupervisorLease
	reservation            AuxiliaryReservationV1
	manifest               Manifest
	need                   vmneed.NeedV1
	spec                   wall.Spec
	domainOwner            *placement
	prepareMu              sync.Mutex
	prepareUsed, confirmed bool
	buildUsed              bool
	started                bool
	engine                 wall.Sandbox
	binding                *microvm.NativeBindingV1
	connection             net.Conn
	correlation            proto.VMPrepareCorrelationV1
	once                   sync.Once
	closeErr               error
}

func PrepareAuxiliaryBootCandidateV1(ctx context.Context, primary string, connection net.Conn) (_ *AuxiliaryBootCandidateV1, returned error) {
	if ctx == nil || !validID(primary) || ctx.Err() != nil {
		return nil, auxiliaryUnavailableV1("the auxiliary candidate has no original owner lifetime or primary")
	}
	manager, err := openNativeUserManagerV1(ctx)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, manager.Close())
		}
	}()
	if err := checkAuxiliarySelfMembershipV1(ctx, manager, primary, 0); err != nil {
		return nil, err
	}
	line, err := newAuxiliaryStartupLineV1(connection)
	if err != nil {
		return nil, err
	}
	peer, err := pinUnverifiedNativeHelperPeerV1(connection)
	if err != nil {
		return nil, errors.Join(err, line.Close())
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.Join(err, peer.Close(), line.Close())
	}
	complete = true
	return &AuxiliaryBootCandidateV1{primary: primary, line: line, peer: peer, manager: manager, nonce: hex.EncodeToString(nonce[:])}, nil
}
func (candidate *AuxiliaryBootCandidateV1) Challenge() string {
	if candidate == nil {
		return ""
	}
	return candidate.nonce
}
func (candidate *AuxiliaryBootCandidateV1) ReadFrame() (proto.Frame, error) {
	if candidate == nil {
		return proto.Frame{}, auxiliaryUnavailableV1("the auxiliary candidate is absent")
	}
	return candidate.line.ReadFrame()
}
func (candidate *AuxiliaryBootCandidateV1) WriteFrame(kind proto.Type, value any) error {
	if candidate == nil {
		return auxiliaryUnavailableV1("the auxiliary candidate is absent")
	}
	return candidate.line.WriteFrame(kind, value)
}

func (candidate *AuxiliaryBootCandidateV1) Accept(ctx context.Context, frame proto.Frame) (_ *AuxiliaryBootV1, returned error) {
	if candidate == nil || ctx == nil {
		return nil, auxiliaryUnavailableV1("the auxiliary loan has no original candidate")
	}
	candidate.mu.Lock()
	defer candidate.mu.Unlock()
	if candidate.used {
		return nil, auxiliaryUnavailableV1("the original auxiliary loan was already consumed")
	}
	candidate.used = true
	var request proto.Request
	var loan auxiliaryLoanV1
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 || request.Op != auxiliaryLoanOperationV1 || len(request.Args) > 1024 || !api.ExactJSONFields(request.Args, loan) || json.Unmarshal(request.Args, &loan) != nil || loan.Version != 1 || loan.Primary != candidate.primary || loan.Nonce != candidate.nonce {
		return nil, errors.Join(auxiliaryUnavailableV1("the auxiliary loan is not the original closed native challenge response"), candidate.line.RejectAncillary())
	}
	files, sender, err := candidate.line.takeAuxiliaryFilesV1()
	if err != nil {
		return nil, err
	}
	defer func() {
		if returned != nil {
			for _, file := range files {
				if file != nil {
					returned = errors.Join(returned, file.Close())
				}
			}
		}
	}()
	if err := errors.Join(candidate.peer.CheckSender(sender), checkBorrowedFlockV1(files[0], sender), checkBorrowedFlockV1(files[1], sender)); err != nil {
		return nil, err
	}
	native, err := adoptStartupOperationFile(ctx, candidate.primary, files[0])
	if err != nil {
		return nil, err
	}
	files[0] = nil
	lifetime, cancel := context.WithCancel(ctx)
	operation := sessionOperationFromNative(lifetime, candidate.primary, native)
	holderNative, err := adoptSupervisorFileLease(ctx, candidate.primary, files[1])
	if err != nil {
		cancel()
		return nil, errors.Join(err, operation.CloseWithError())
	}
	files[1] = nil
	holder := &OwnedSupervisorLease{native: holderNative, lifetime: lifetime, cancel: cancel, generation: loan.Token}
	result := &AuxiliaryBootV1{ctx: lifetime, cancel: cancel, candidate: candidate, operation: operation, holder: holder, disk: files[2]}
	files[2] = nil
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, result.Close())
		}
	}()
	result.diskParent, err = cachefs.OpenPrivateChild(operation.native.root, "vm", false)
	if err != nil {
		return nil, err
	}
	result.diskParentRef, err = cachefs.CaptureRoot(result.diskParent)
	if err != nil {
		return nil, err
	}
	result.diskRef, err = cachefs.CaptureFile(result.diskParent, microvm.SessionDiskName, result.disk, 0600)
	if err != nil {
		return nil, err
	}
	snapshot, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.ID != candidate.primary || snapshot.SandboxOf != "" || snapshot.Wall == nil || snapshot.Wall.Level != "microvm" || snapshot.Auxiliary == nil || validateAuxiliaryReservationV1Data(snapshot.Auxiliary) != nil || snapshot.Auxiliary.Token != loan.Token || snapshot.Auxiliary.BootID != BootID() || startupDigest(snapshot.VMNeedV1) != loan.Need {
		return nil, auxiliaryUnavailableV1("the borrowed original auxiliary source/reservation differs from its native loan")
	}
	runtimeBytes, err := startupRuntimeBytes(snapshot)
	if err != nil || startupDigest(runtimeBytes) != loan.Runtime {
		return nil, errors.Join(auxiliaryUnavailableV1("the borrowed original auxiliary runtime differs from its native loan"), err)
	}
	need, err := vmneed.DecodeV1(snapshot.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := need.ValidateWall(runtime.GOARCH, snapshot.Wall.Level, snapshot.Wall.Driver, snapshot.Limits.MemoryMax); err != nil {
		return nil, err
	}
	if snapshot.Auxiliary.Declared != snapshot.Limits.MemoryMax {
		return nil, auxiliaryUnavailableV1("the auxiliary native declared cap differs from its original protected Need")
	}
	if err := checkAuxiliarySelfMembershipV1(ctx, candidate.manager, candidate.primary, snapshot.Auxiliary.Declared); err != nil {
		return nil, err
	}
	result.ctx = context.WithValue(lifetime, auxiliaryBootContextKeyV1{}, result)
	result.domainOwner, err = placementForCall()
	if err != nil {
		return nil, err
	}
	if err := result.domainOwner.checkIdentity(); err != nil {
		return nil, err
	}
	result.reservation = *cloneAuxiliaryReservationV1(snapshot.Auxiliary)
	result.manifest = CloneManifest(*snapshot)
	result.need = need.Clone()
	boot := need.BootV1.Clone()
	result.spec = wall.Spec{Driver: need.Driver, ID: snapshot.ID, SessionDir: operation.native.path, MemoryMax: snapshot.Limits.MemoryMax, CPUs: need.CPUs, DiskMax: need.DiskMax, VM: &boot}
	if err := result.Check(result.ctx); err != nil {
		return nil, err
	}
	if err := candidate.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true, Result: json.RawMessage(`{"operation_nonce":"` + candidate.nonce + `"}`)}); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func checkAuxiliarySelfMembershipV1(ctx context.Context, manager *nativeUserManagerV1, primary string, declared int64) error {
	if manager == nil {
		return auxiliaryUnavailableV1("the closed auxiliary helper has no native manager")
	}
	if err := manager.checkNativeManager(); err != nil {
		return err
	}
	unit, err := AuxUnitNameV1(primary)
	if err != nil {
		return err
	}
	var path dbus.ObjectPath
	if err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".GetUnitByPID", 0, uint32(os.Getpid())).Store(&path); err != nil {
		return err
	}
	observed, err := manager.activation(ctx, path, unit)
	if err != nil {
		return err
	}
	if observed.MainPID != uint32(os.Getpid()) {
		return auxiliaryUnavailableV1("the closed auxiliary process is not its actual native exec activation")
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	expected := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", os.Getuid(), os.Getuid(), brand.Slice, unit)
	if !bytes.Equal(data, []byte(expected)) {
		return auxiliaryUnavailableV1("the closed auxiliary helper has another native cgroup")
	}
	native, err := readAuxMemoryUnitV1(ctx, primary)
	if err != nil {
		return err
	}
	if !native.exists || native.maximum <= 0 || native.swap != 0 || declared > 0 && native.maximum != declared {
		return auxiliaryUnavailableV1("the closed auxiliary helper lacks its exact native cap and zero swap")
	}
	return errors.Join(manager.checkNativeManager(), ctx.Err())
}
