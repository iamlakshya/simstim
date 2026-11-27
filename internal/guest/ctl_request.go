package guest

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

// request answers one op of the supervisor.
func (g *Init) request(w *proto.Writer, req proto.Request) {
	var finish func()
	defer func() {
		if finish != nil {
			finish()
		}
	}()
	reply := func(v any, err error) {
		if finish != nil {
			finish() // filesystem receipt ends BEFORE potentially stalled reply I/O
			finish = nil
		}
		resp := proto.Response{ID: req.ID, OK: err == nil}
		if err != nil {
			var pe *proto.Error
			if !errors.As(err, &pe) {
				pe = &proto.Error{Code: "wall_init", Message: err.Error(), Hint: "Read the sandbox's log, then resume the session."}
			}
			resp.Error = pe
		} else {
			resp.Result, _ = json.Marshal(v)
		}
		encoded, encodeErr := json.Marshal(resp)
		if encodeErr != nil || len(encoded)+1 > maxEvent {
			resp = proto.Response{ID: req.ID, OK: false, Error: &proto.Error{Code: "pull_metadata", Message: "the actual control response exceeds its frame bound", Hint: "Use metadata representable in the existing control protocol."}}
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	if g.purgeReader != nil {
		g.purgeReader.request(req, reply)
		return
	}
	if g.imageSaveControlV1(req, reply) {
		return
	}
	if g.freezeControlV1(req, reply) {
		return
	}
	if g.vm != nil {
		var err error
		finish, err = g.vm.controlOperation()
		if err != nil {
			reply(nil, err)
			return
		}
	}
	switch req.Op {
	case proto.CtlRefsV1:
		result, err := g.snapshotRefsV1(req.Args)
		reply(result, err)
	case proto.CtlForkBranchesV1:
		var args proto.CtlForkBranchesArgsV1
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil {
			reply(nil, snapshotGitErrorV1("the fork branch request is invalid"))
			return
		}
		reply(struct{}{}, g.renameForkBranchesV1(args.Parent, args.Child))
	case proto.CtlPortsV1:
		reply(g.portClaims(), nil)
	case proto.CtlReverseStartV1:
		reply(struct{}{}, g.startReverse(req.Args))
	case proto.CtlReverseStopV1:
		reply(struct{}{}, g.stopReverse(req.Args))
	case inputtransfer.CtlPrepare:
		err := g.prepareInputs(req.Args)
		reply(struct{}{}, err)
	case inputtransfer.CtlStatus:
		result, err := g.inputStatus(req.Args)
		reply(result, err)
	case inputtransfer.CtlCancel:
		var scope inputtransfer.Scope
		if err := json.Unmarshal(req.Args, &scope); err != nil {
			reply(nil, err)
			return
		}
		reply(struct{}{}, g.cancelInputs(scope))
	case proto.CtlMemberPrepare:
		result, err := g.prepareMember(req.Args)
		reply(result, err)
	case proto.CtlMemberResumePrepare:
		result, err := g.prepareMemberResume(req.Args)
		reply(result, err)
	case proto.CtlMemberAbort:
		result, err := g.abortPreparedMember(req.Args)
		reply(result, err)
		g.endIfEmpty()
	case proto.CtlMemberStore:
		reply(struct{}{}, g.storePreparedMember(req.Args))
	case proto.CtlGuestCapabilities:
		reply(proto.CtlGuestFeatures{Version: 1, Features: g.imageSaveGuestFeaturesV1([]string{proto.GuestSessionPrepareV1, proto.GuestMemberPrepareV1, proto.FeatureDecoyFilesV1, proto.FeaturePackageManagersV1, inputtransfer.Feature, inputtransfer.PrimaryPolicyFeature, api.FeatureBoundSecrets, api.FeatureNamedSSHKeysV1})}, nil)
	case proto.CtlPrepare:
		result, err := g.prepareController(req.Args)
		reply(result, err)
	case proto.CtlVMPrepare:
		if g.vm == nil {
			reply(nil, vmError("VM prepare is refused outside the actual microvm init"))
			return
		}
		plan, err := vmconfig.Decode(req.Args)
		if err != nil {
			reply(nil, err)
			return
		}
		result, err := g.vm.prepare(plan.Args)
		reply(result, err)
	case proto.CtlPurgeWorkInspectV1:
		result, err := g.inspectPurgeWorkV1(req.Args)
		reply(result, err)
	case proto.CtlBundlePrepare:
		var a proto.CtlBundleArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, err)
			return
		}
		// Reserve synchronously before the next ordered control frame can
		// cancel. Git work uses the same reader concurrently, not another parser.
		op, err := g.beginBundle(a)
		if err != nil {
			reply(nil, err)
			return
		}
		bundleFinish := finish
		finish = nil // transfer this SAME receipt to the asynchronous operation
		go func() {
			defer func() {
				if bundleFinish != nil {
					bundleFinish()
				}
			}()
			result, err := g.prepareBundleOperation(op, a)
			if bundleFinish != nil {
				bundleFinish()
				bundleFinish = nil
			}
			reply(result, err)
		}()
	case proto.CtlBundleCancel:
		var a struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, err)
			return
		}
		g.cancelBundle(a.ID)
		reply(struct{}{}, nil)
	case proto.CtlMembersReady:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before member readiness"))
			return
		}
		g.mu.Lock()
		g.pendingBoot = 0
		g.bootAdmission = false
		g.mu.Unlock()
		reply(struct{}{}, nil)
		g.endIfEmpty()
	case proto.CtlSpawn:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before a member starts"))
			return
		}
		a, err := g.decodeMember(req.Args)
		if err != nil {
			reply(nil, err)
			return
		}
		pid, err := g.spawn(a)
		reply(proto.CtlStartResult{AgentPID: pid, GuestABI: g.ptyABI()}, err)
	case proto.CtlStopMember:
		var a proto.CtlMemberArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, err)
			return
		}
		reply(struct{}{}, g.stopMember(a.Session))
	case proto.CtlEditorPrepareV1:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before editor operations"))
			return
		}
		reply(struct{}{}, g.prepareEditorV1(req.Args))
	case proto.CtlEditorCancelV1:
		reply(struct{}{}, g.cancelEditorV1(req.Args))
	case proto.CtlExec:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before commands"))
			return
		}
		var a proto.CtlExecArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, err)
			return
		}
		reply(struct{}{}, g.registerExec(a))
	case proto.CtlSeed:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before seed"))
			return
		}
		var a proto.CtlSeedArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, err)
			return
		}
		result, err := g.seed(a)
		reply(result, err)
	case proto.CtlStart:
		if g.vm != nil && !g.vm.prepared() {
			reply(nil, vmError("the VM must be prepared before start"))
			return
		}
		a, err := g.decodeMember(req.Args)
		if err != nil {
			reply(nil, fmt.Errorf("read start: %w", err))
			return
		}
		if a.ForkOf != "" {
			if err := g.renameForkBranchesV1(a.ForkOf, a.Session); err != nil {
				reply(nil, err)
				return
			}
		}
		g.mu.Lock()
		prepared := a.PreparedClaim != "" && g.preparedMatchesLocked(a, proto.CtlStart, memberReady)
		busy := g.ended || g.starting || g.holder != nil || a.PreparedClaim != "" && !prepared || a.PreparedClaim == "" && (!g.inputStartAllowedLocked(a) || g.memberStarts[a.Session])
		g.starting = !busy
		if !busy {
			g.primary = a.Session
			g.pendingBoot = a.PendingMembers
			g.bootAdmission = a.PendingMembers > 0
			g.memberStarts[a.Session] = true
		}
		g.mu.Unlock()
		if busy {
			reply(nil, &proto.Error{Code: "busy", Message: "the sandbox has started its session already",
				Hint: "Stop the session, then resume it."})
			return
		}
		pid, err := g.start(a)
		reply(proto.CtlStartResult{AgentPID: pid, GuestABI: g.ptyABI()}, err)
		g.mu.Lock()
		g.starting = false
		delete(g.memberStarts, a.Session)
		hp := g.holder
		g.mu.Unlock()
		// A holder that exited while it started ends the guest init only
		// now, after the answer that says why.
		if err != nil || hp.exited() {
			g.end()
		} else {
			g.endIfEmpty()
		}
	case proto.CtlStop:
		var a proto.CtlStopArgs
		_ = json.Unmarshal(req.Args, &a)
		g.mu.Lock()
		g.cleanStop = true
		g.mu.Unlock()
		reply(struct{}{}, nil)
		go g.stop(time.Duration(a.GraceMS) * time.Millisecond)
	default:
		reply(nil, &proto.Error{Code: "unknown_op", Message: "the guest init does not know " + req.Op,
			Hint: "Stop the session, then resume it, so that its sandbox runs the supervisor's version."})
	}
}
