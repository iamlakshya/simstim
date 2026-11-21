package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"github.com/iamlakshya/simstim/internal/wall/none"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// nativeStopScopeV1 belongs to one actual outside connection and this same
// running original supervisor. It does not reconstruct a process from data.
type nativeStopScopeV1 struct {
	noneStop           *noneStopCompletionV1
	none               *none.LiveOwnerV1
	warmMutation       bool
	warmUncertain      error
	mu                 sync.Mutex
	line               *host.StartupLineV1
	operation          *host.SessionOperation
	warmCancel         context.CancelFunc
	warmDone           chan struct{}
	released           bool
	snapshotStarted    bool
	snapshotCancel     context.CancelFunc
	snapshotDone       chan struct{}
	reservationStarted bool
	reservationCancel  context.CancelFunc
	reservationDone    chan struct{}
	fork               *nativeForkJobV1

	root       *Supervisor
	owner      *host.OwnedSupervisorLease
	runtime    sandbox
	cleaned    <-chan struct{}
	state      proto.NativeStopStateV1
	purpose    string
	used       bool
	lifetime   context.Context
	native     *host.OriginalVMNeed
	connection net.Conn
	process    *process.CopyOwnerV1
	gvisor     *gvisor.LiveOwnerV1
}

func init() { Features = append(Features, proto.FeatureNativeStopV1) }
func (s *Supervisor) nativeStopRequestV1(client *client, request proto.Request) bool {
	if s.managerCustodyStopRequestV1(client, request) {
		return true
	}
	if request.Op != proto.NativeStopInspectV1 && request.Op != proto.NativeStopCommitV1 && request.Op != proto.NativeResumeInspectV1 && request.Op != proto.NativeResumeCheckV1 {
		return false
	}
	root := s.owner()
	var args proto.NativeStopArgsV1
	if !client.full || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != root.id || !supply.IsHex64(args.Nonce) {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the native stop has no exact original outside scope")))
		return true
	}
	if request.Op == proto.NativeResumeCheckV1 {
		scope := client.nativeScopeV1()
		if scope == nil || scope.purpose != proto.NativeResumeInspectV1 || scope.state.Nonce != args.Nonce {
			client.reply(request.ID, nil, toProtoErr(execRefusal("the original warm inspection scope is absent")))
			return true
		}
		if err := scope.checkCurrentV1(root.lifetimeContext()); err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		client.reply(request.ID, scope.state, nil)
		return true
	}
	root.mu.Lock()
	owner := root.bootOwner
	live := owner != nil && owner.Valid(root.id) && owner.StartupCommittedV1() && root.sb != nil && root.launched && root.runtimeCleaned != nil && !channelClosed(root.runtimeCleaned) && root.generation != ""
	if !live {
		root.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the current supervisor has no supported original native lifetime")))
		return true
	}
	runtimeBytes, err := json.Marshal(root.m.RuntimeManifest)
	if root.m.RuntimeManifest == nil {
		runtimeBytes = nil
	}
	if err != nil {
		root.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	digest := func(raw []byte) string {
		if len(raw) == 0 {
			return ""
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	state := proto.NativeStopStateV1{Version: 1, Primary: root.id, Nonce: args.Nonce, Generation: root.generation, Need: digest(root.m.VMNeedV1), Runtime: digest(runtimeBytes)}
	if request.Op == proto.NativeStopInspectV1 || request.Op == proto.NativeResumeInspectV1 {
		if client.nativeScopeV1() != nil {
			root.mu.Unlock()
			client.reply(request.ID, nil, toProtoErr(execRefusal("this connection already owns a native stop scope")))
			return true
		}
		scope := &nativeStopScopeV1{root: root, owner: owner, runtime: root.sb, cleaned: root.runtimeCleaned, state: state, purpose: request.Op, lifetime: root.lifetimeContext(), native: host.OriginalVMNeedFromContext(root.lifetimeContext()), connection: root.vmStartConnection, process: root.processCopy, gvisor: root.gvisorLive, none: root.noneLive}
		client.setNativeScopeV1(scope)
		root.mu.Unlock()
		if request.Op == proto.NativeResumeInspectV1 {
			if err := scope.checkNativeRuntimeV1(root.lifetimeContext()); err != nil {
				client.setNativeScopeV1(nil)
				client.reply(request.ID, nil, toProtoErr(err))
				return true
			}
		}
		if request.Op == proto.NativeResumeInspectV1 {
			if client.frameReader == nil || client.frameReader.Buffered() != 0 {
				client.setNativeScopeV1(nil)
				client.reply(request.ID, nil, toProtoErr(execRefusal("the native warm lane has buffered or unowned input")))
				return true
			}
			line, err := host.NewNativeStartupLineV1(client.conn)
			if err != nil {
				client.setNativeScopeV1(nil)
				client.reply(request.ID, nil, toProtoErr(err))
				return true
			}
			client.nativeFrameReader = line
			scope.mu.Lock()
			scope.line = line
			scope.mu.Unlock()
		}
		if err := client.replyNativeStopProofV1(owner, request.ID, state); err != nil {
			client.close()
		}
		return true
	}
	scope := client.nativeScopeV1()
	valid := scope != nil && scope.purpose == proto.NativeStopInspectV1 && scope.root == root && scope.owner == owner && scope.cleaned == root.runtimeCleaned && scope.state == state
	if valid {
		client.setNativeScopeV1(nil)
	}
	root.mu.Unlock()
	if !valid {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the original native stop continuation changed")))
		return true
	}
	if err := root.retainManagerCustodyForSameRequestStopV1(scope.lifetime, scope.owner); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	// This means only that this original live owner accepted a stop. The
	// outside owner must still join actual pidfd and whole native unit absence.
	root.requestTrustedStopV1()
	client.reply(request.ID, state, nil)
	root.signalTrustedStopV1()
	return true
}
