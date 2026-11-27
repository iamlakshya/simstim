//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

// ordinaryHolderCaptureV2 retains an actual authenticated live holder. It
// supplies no stopped or storage authority before its physical join.
type ordinaryHolderCaptureV2 struct {
	mu                        sync.Mutex
	operation                 *SessionOperation
	retainedOrigin            *purgeRetainedOriginV2
	origin                    *placement
	purgeImage                *purgeAttemptImageV2
	holder                    *sessionFileLease
	line                      *StartupLineV1
	peer                      *nativeStartupPeerV1
	main, joined              *os.File
	managed                   *ordinaryInvocationV1
	socket                    os.FileInfo
	socketRoot                *os.Root
	socketRootRef             cachefs.Reference
	holderInfo                os.FileInfo
	source                    Manifest
	state                     ordinaryStopStateV1
	boot                      string
	stopSent, settled, closed bool
	restored                  *os.File
	pauseSent                 bool
}

func ordinaryManifestV1(m *Manifest) bool {
	return m != nil && m.Wall == nil && m.SandboxOf == "" && m.Files == nil && m.Auxiliary == nil && m.RuntimeManifest == nil && len(m.VMNeedV1) == 0 && m.Guest == nil && m.Agents == ""
}

func captureOrdinaryHolderV1(ctx context.Context, operation *SessionOperation, reversible bool) (*ordinaryHolderCaptureV2, error) {
	owner, err := captureOrdinaryHolderV2(ctx, operation, reversible)
	if err != nil && owner != nil {
		// This legacy signature cannot return an unready stopped subject. Keep
		// the SAME original stack until its unused native capture really closes.
		for {
			if closeErr := owner.Close(); closeErr == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil, err
	}
	return owner, err
}

func captureOrdinaryHolderV2(ctx context.Context, operation *SessionOperation, reversible bool) (result *ordinaryHolderCaptureV2, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("ordinary stop has no original owning primary operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	source, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !ordinaryManifestV1(source) || source.Run.BootID != BootID() || !auxiliaryBootIDV1(BootID()) {
		return nil, sessionLeaseError("the ordinary session has no supported current-boot producer; preserve its unknown lifetime")
	}
	origin, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if err := origin.checkIdentity(); err != nil {
		return nil, err
	}
	owner := &ordinaryHolderCaptureV2{operation: operation, origin: origin, source: CloneManifest(*source), boot: BootID()}
	if reversible {
		owner.purgeImage, err = ordinaryPurgeImageForPlacementV3(ctx, origin)
		if err != nil {
			return nil, err
		}
	}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	owner.socketRoot, err = cachefs.OpenPrivateRoot(filepath.Dir(paths.Socket(operation.Primary())), false)
	if err != nil {
		return nil, err
	}
	owner.socketRootRef, err = cachefs.CaptureRoot(owner.socketRoot)
	if err != nil {
		return nil, err
	}
	owner.socket, err = owner.socketRoot.Lstat(filepath.Base(paths.Socket(operation.Primary())))
	if err != nil || owner.socket.Mode()&os.ModeSocket == 0 {
		return nil, errors.Join(sessionLeaseError("the actual ordinary holder has no original native socket"), err)
	}
	directory, err := owner.socketRoot.Open(".")
	if err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", fmt.Sprintf("/proc/self/fd/%d/%s", directory.Fd(), filepath.Base(paths.Socket(operation.Primary()))))
	err = errors.Join(err, directory.Close())
	if err != nil {
		return nil, err
	}
	owner.line, err = NewNativeStartupLineV1(connection)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	} else {
		_ = owner.line.SetDeadline(time.Now().Add(10 * time.Second))
	}
	role, responseRole := ordinaryStopRoleV1, ordinaryHolderRoleV1
	if reversible {
		role, responseRole = ordinaryPurgeRoleV2, ordinaryPurgeHolderRoleV2
	}
	outgoing := proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: role}
	if err := owner.writeOrdinaryCaptureHelloV3(ctx, outgoing); err != nil {
		return nil, err
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := owner.acceptOrdinaryCaptureHelloV3(ctx, frame); err != nil {
		return nil, err
	}
	var hello proto.Hello
	if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != responseRole {
		return nil, sessionLeaseError("the current ordinary socket has no exact native producer hello")
	}
	owner.peer = owner.line.nativePeer
	if err := owner.checkCurrentOrdinaryV1(ctx); err != nil {
		return nil, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	owner.state = ordinaryStopStateV1{Version: 1, Primary: operation.Primary(), Nonce: hex.EncodeToString(nonce[:])}
	if err := owner.ordinaryRequestV1(ctx, 1, ordinaryStopInspectV1, owner.state); err != nil {
		return nil, err
	}
	file, err := owner.receiveOrdinaryFileV1(ctx)
	if err != nil {
		return nil, err
	}
	owner.holder, err = adoptOrdinaryHolderFileV1(ctx, operation.Primary(), file)
	if err != nil {
		return nil, err
	}
	if err := operation.native.rootRef.CheckRoot(owner.holder.root); err != nil {
		return nil, err
	}
	owner.holderInfo, err = owner.holder.file.Stat()
	if err != nil {
		return nil, err
	}
	owner.main, err = owner.receiveOrdinaryFileV1(ctx)
	if err != nil {
		return nil, err
	}
	if err := unix.PidfdSendSignal(int(owner.main.Fd()), 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return nil, err
	}
	owner.joined, err = owner.receiveOrdinaryFileV1(ctx)
	if err != nil {
		return nil, err
	}
	if err := ordinaryJoinLatchV1(owner.joined, false); err != nil {
		return nil, err
	}
	if reversible {
		owner.restored, err = owner.receiveOrdinaryFileV1(ctx)
		if err != nil {
			return nil, err
		}
		if err := ordinaryJoinLatchV1(owner.restored, false); err != nil {
			return nil, err
		}
	}
	owner.managed, err = captureOrdinaryInvocationV1(ctx, owner)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(owner.checkCurrentOrdinaryV1(ctx), owner.checkOriginalSocketV1()); err != nil {
		return nil, err
	}
	if err := bindPurgeOrdinaryOriginV2(ctx, owner); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *ordinaryHolderCaptureV2) checkCurrentOrdinaryV1(ctx context.Context) error {
	if owner == nil || owner.peer == nil || owner.peer.identity == nil || owner.origin == nil {
		return sessionLeaseError("the ordinary stop has no original native code owner")
	}
	raw, err := owner.peer.proc.ReadFile("cmdline")
	if err != nil {
		return err
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(argv) != 3 || argv[1] != "holder" || argv[2] != owner.operation.Primary() {
		return sessionLeaseError("the current ordinary helper has another closed command")
	}
	expected := owner.origin.executableRef
	if owner.purgeImage != nil {
		if err := owner.purgeImage.Check(ctx); err != nil {
			return err
		}
		expected = owner.purgeImage.originalInfo
	}
	if err := errors.Join(ctx.Err(), owner.origin.checkIdentity(), checkNativeHelperExecutableV1(owner.peer, expected, argv)); err != nil {
		return err
	}
	if owner.managed != nil {
		return owner.managed.Check(ctx)
	}
	return owner.peer.checkLive()
}
func (owner *ordinaryHolderCaptureV2) checkOriginalSocketV1() error {
	if owner.socketRoot == nil {
		return sessionLeaseError("the ordinary socket has no original native parent")
	}
	if err := owner.socketRootRef.CheckRoot(owner.socketRoot); err != nil {
		return err
	}
	fresh, err := cachefs.OpenPrivateRoot(filepath.Dir(paths.Socket(owner.operation.Primary())), false)
	if err != nil {
		return err
	}
	if err := errors.Join(owner.socketRootRef.CheckRoot(fresh), fresh.Close()); err != nil {
		return err
	}
	actual, err := owner.socketRoot.Lstat(filepath.Base(paths.Socket(owner.operation.Primary())))
	if err != nil || !os.SameFile(actual, owner.socket) {
		return errors.Join(sessionLeaseError("the original ordinary holder socket was replaced"), err)
	}
	return nil
}

func (owner *ordinaryHolderCaptureV2) ordinaryRequestV1(ctx context.Context, id uint64, operation string, state ordinaryStopStateV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	} else {
		_ = owner.line.SetDeadline(time.Now().Add(10 * time.Second))
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = owner.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only this actual physical commit attempt starts settlement retention.
	// Cancellation before dispatch closes the unused original proof normally.
	if operation == ordinaryStopCommitV1 {
		owner.stopSent = true
	}
	if operation == ordinaryPurgeQuiesceV2 {
		owner.pauseSent = true
	}
	if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: id, Op: operation, Args: raw}); err != nil {
		return err
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return err
	}
	sender, err := owner.line.currentSender()
	if err != nil {
		return err
	}
	if err := owner.peer.checkNativeSender(sender); err != nil {
		return err
	}
	var response proto.Response
	var received ordinaryStopStateV1
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK || !api.ExactJSONFields(response.Result, received) || json.Unmarshal(response.Result, &received) != nil {
		return sessionLeaseError("the actual ordinary holder supplied no exact continuation response")
	}
	if id == 1 {
		if received.Version != state.Version || received.Primary != state.Primary || received.Nonce != state.Nonce || len(received.Generation) != 64 {
			return sessionLeaseError("the ordinary inspection changed its original continuation")
		}
		owner.state = received
	} else if received != state {
		return sessionLeaseError("the actual ordinary closure acknowledges another continuation")
	}
	return nil
}

func (owner *ordinaryHolderCaptureV2) receiveOrdinaryFileV1(ctx context.Context) (_ *os.File, returned error) {
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, err
	}
	var response proto.Response
	var state ordinaryStopStateV1
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 || !response.OK || !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != owner.state {
		return nil, errors.Join(sessionLeaseError("the original ordinary private role changed its continuation"), owner.line.RejectAncillary())
	}
	file, sender, err := owner.line.takeLoanFile()
	if err != nil {
		return nil, err
	}
	if err := errors.Join(owner.peer.checkNativeSender(sender), owner.checkCurrentOrdinaryV1(ctx)); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (owner *ordinaryHolderCaptureV2) settleOrdinaryV1() error {
	if owner == nil || !owner.stopSent || owner.peer == nil || owner.main == nil || owner.joined == nil || owner.holder == nil {
		return sessionLeaseError("the ordinary stop has no original native settlement source")
	}
	if owner.settled {
		return nil
	}
	// Neither cancellation nor a deadline turns unknown physical closure into
	// permission. Keep the SAME owning stack while those original objects join.
	for {
		if ordinaryJoinLatchV1(owner.joined, true) == nil && ordinaryPidfdExitedV1(owner.main) == nil && ordinaryPeerExitedV1(owner.peer) == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if owner.managed != nil {
		if err := owner.managed.settle(); err != nil {
			return err
		}
	}
	owner.settled = true
	return (&StoppedOrdinarySessionV1{ordinaryHolderCaptureV2: owner}).checkStoppedOrdinaryNativeFactsV1(context.Background())
}

func ordinaryPeerExitedV1(peer *nativeStartupPeerV1) error {
	if peer == nil || peer.pidfd < 0 {
		return sessionLeaseError("the ordinary holder has no original native process handle")
	}
	polls := []unix.PollFd{{Fd: int32(peer.pidfd), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(sessionLeaseError("the original ordinary holder has no positive native exit"), err)
	}
	return nil
}

func (owner *ordinaryHolderCaptureV2) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.stopSent && !owner.settled {
		if err := owner.settleOrdinaryV1(); err != nil {
			return err
		}
	}
	owner.closed = true
	var failure error
	if owner.line != nil {
		failure = errors.Join(failure, owner.line.Close())
	}
	if owner.holder != nil {
		failure = errors.Join(failure, owner.holder.Close())
	}
	if owner.socketRoot != nil {
		failure = errors.Join(failure, owner.socketRoot.Close())
	}
	if owner.main != nil {
		failure = errors.Join(failure, owner.main.Close())
	}
	if owner.joined != nil {
		failure = errors.Join(failure, owner.joined.Close())
	}
	if owner.managed != nil {
		failure = errors.Join(failure, owner.managed.Close())
	}
	if owner.restored != nil {
		failure = errors.Join(failure, owner.restored.Close())
	}
	return failure
}
