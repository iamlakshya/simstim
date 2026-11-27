//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func (launch *startupLaunchV1) sendOperationLoan(ctx context.Context) (failure error) {
	launch.mu.Lock()
	defer launch.mu.Unlock()
	if launch.loanSent || launch.line == nil || launch.operation == nil || launch.operation.native.borrowed {
		return sessionLeaseError("the original startup loan was already sent or has no owning operation")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	if launch.nativePeer() == nil || launch.nativePeer().identity == nil {
		return sessionLeaseError("the actual original helper identity has not arrived before its private OP loan")
	}
	m, err := launch.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(m.VMNeedV1, launch.manifest.VMNeedV1) || !reflect.DeepEqual(m.RuntimeManifest, launch.manifest.RuntimeManifest) ||
		m.Limits != launch.manifest.Limits || m.Wall == nil || !reflect.DeepEqual(m.Wall, launch.manifest.Wall) {
		return sessionLeaseError("the original protected startup source changed before handoff")
	}
	runtimeBytes, err := startupRuntimeBytes(m)
	if err != nil {
		return err
	}
	loan := startupLoanV1{Version: 1, Primary: m.ID, Nonce: launch.hello.StartupNonce, Purpose: "ordinary", Need: startupDigest(m.VMNeedV1), Runtime: startupDigest(runtimeBytes)}
	if launch.operation.IsSandboxPrepareV1() {
		if err := validateSandboxPrepareBootManifestV1(m); err != nil {
			return err
		}
		loan.Purpose = startupLoanPurposeSandboxPrepareV1
		parent, err := launch.operation.sandboxPrepare.originalParentComparisonV1(launch.operation)
		if err != nil {
			return err
		}
		loan.PrepareParent = &parent
	} else if len(m.AutomaticRestore) != 0 {
		boot := launch.operation.AutomaticBoot()
		if boot == nil {
			return automaticRestoreError()
		}
		boot.mu.Lock()
		if boot.used || boot.operation != launch.operation || !bytes.Equal(boot.plan, m.AutomaticRestore) {
			boot.mu.Unlock()
			return automaticRestoreError()
		}
		if _, err := ValidateAutomaticRestore(m); err != nil {
			boot.mu.Unlock()
			return err
		}
		// Transfer the same one-use automatic intent; the outside owner cannot
		// also consume it after lending it to the original startup process.
		boot.used = true
		boot.mu.Unlock()
		loan.Purpose, loan.Automatic = "automatic", startupDigest(m.AutomaticRestore)
	}
	if p := launch.operation.rewindPreparation; p != nil && p.slotSnapshot != nil {
		return launch.sendSlotReservedStartupV2Locked(ctx, m, loan, p)
	}
	file, err := launch.operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, file.Close()) }()
	args, err := json.Marshal(loan)
	if err != nil {
		return err
	}
	if err := launch.placement.gateControl(ctx, func(control *placementControl) error {
		if err := launch.operation.native.check(ctx); err != nil {
			return err
		}
		if err := launch.checkNativeMemoryV1(ctx, m); err != nil {
			return err
		}
		if launch.operation.nativeColdResume != nil {
			if err := checkColdNativeTakeoverLockedV1(ctx, launch.operation); err != nil {
				return err
			}
			if err := takeoverResumePreparationNativeLockedV1(ctx, launch.operation, launch.operation.nativeColdResume, control); err != nil {
				return err
			}
		}
		if err := checkNoneColdTakeoverV1(ctx, launch.operation); err != nil {
			return err
		}
		if err := launch.queueSandboxPrepareReplyV1(1); err != nil {
			return err
		}
		if loan.Purpose == startupLoanPurposeSandboxPrepareV1 {
			parent, err := launch.operation.sandboxPrepare.originalParentComparisonV1(launch.operation)
			if err != nil || loan.PrepareParent == nil || parent.Identity != loan.PrepareParent.Identity {
				return errors.Join(sessionLeaseError("the original prepare parent changed at the physical loan send"), err)
			}
		}
		launch.loanSent = true
		return launch.line.writeLoan(proto.Request{ID: 1, Op: startupLoanOperationV1, Args: args}, file)
	}); err != nil {
		return err
	}
	result, err := launch.response(ctx, 1, true)
	if err != nil {
		return err
	}
	var ack struct {
		Nonce string `json:"startup_nonce"`
	}
	if !api.ExactJSONFields(result, ack) || json.Unmarshal(result, &ack) != nil || ack.Nonce != loan.Nonce {
		return sessionLeaseError("the original native supervisor did not acknowledge this exact loan")
	}
	return launch.check(ctx)
}

func (launch *startupLaunchV1) response(ctx context.Context, requestID uint64, live bool) (_ json.RawMessage, failure error) {
	consumed := false
	defer func() { launch.finishSandboxPrepareReplyV1(requestID, consumed, failure) }()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = launch.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if end, ok := ctx.Deadline(); ok {
		_ = launch.line.SetDeadline(end)
	} else {
		_ = launch.line.SetDeadline(time.Time{})
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, err := launch.line.ReadFrame()
		if err != nil {
			if launch.line.prepareCursor == nil || launch.line.preparePoison || !sandboxPrepareReadDeadlineV1(err) {
				launch.readFailed = true
			}
			return nil, err
		}
		if err := launch.line.RejectAncillary(); err != nil {
			return nil, err
		}
		sender, err := launch.line.currentSender()
		if err != nil {
			return nil, err
		}
		native := launch.nativePeer()
		if native == nil || sender.pid != native.pid || sender.uid != native.uid {
			return nil, sessionLeaseError("the startup response has another native sender")
		}
		if live && launch.line.prepareCursor == nil {
			if err := launch.check(ctx); err != nil {
				return nil, err
			}
		}
		if frame.Type == proto.TError {
			var failure proto.Error
			if json.Unmarshal(frame.Payload, &failure) == nil {
				return nil, &failure
			}
			return nil, sessionLeaseError("the original native supervisor refused startup")
		}
		if frame.Type != proto.TResponse {
			return nil, sessionLeaseError("startup received an unexpected frame on its original connection")
		}
		var response proto.Response
		if json.Unmarshal(frame.Payload, &response) != nil || response.ID != requestID {
			return nil, sessionLeaseError("startup received another request's response")
		}
		if launch.line.prepareCursor != nil && !api.ExactJSONFields(frame.Payload, response) {
			return nil, sessionLeaseError("the original preparation response has unknown or duplicate fields")
		}
		if launch.line.prepareCursor != nil && response.OK && response.Error != nil {
			return nil, sessionLeaseError("the original preparation response mixed success and failure")
		}
		consumed = true
		if live && launch.line.prepareCursor != nil {
			if err := launch.check(ctx); err != nil {
				return nil, err
			}
		}
		if !response.OK {
			if response.Error != nil {
				return nil, response.Error
			}
			return nil, sessionLeaseError("the original native supervisor refused the startup request")
		}
		return response.Result, nil
	}
}

func (launch *startupLaunchV1) deliverStart(ctx context.Context, env map[string]api.Secret, members []api.MemberResumeArgs, allowMissing bool, delivery *api.SecretDeliveryIntent) error {
	launch.mu.Lock()
	defer launch.mu.Unlock()
	if !launch.loanSent || launch.ready || launch.line == nil {
		return sessionLeaseError("start delivery has no original accepted native loan")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	if err := requireSupervisorFeatures(launch.hello, api.FeatureSpawn, api.FeatureSandboxSnapshot); err != nil {
		return err
	}
	if delivery != nil {
		if err := requireSupervisorFeatures(launch.hello, api.FeatureBoundSecrets); err != nil {
			return err
		}
	}
	var namedKeys *api.NamedSSHKeysV1
	var protected map[string]api.Secret
	var err error
	if p := launch.operation.rewindPreparation; p != nil {
		namedKeys, protected, members, err = p.takeStartupDeliveryV1(ctx, launch, delivery, members)
	} else {
		namedKeys, err = launch.operation.takeNamedSSHKeys(ctx)
		if err == nil {
			protected, members, err = launch.operation.takeProtectedTrustedSecretsV1(ctx, delivery, members)
		}
	}
	defer clear(protected)
	defer clearProtectedTrustedMembersV1(members)
	if namedKeys != nil {
		defer clear(namedKeys.Values)
	}
	if err != nil {
		return err
	}
	if namedKeys != nil {
		if err := requireSupervisorFeatures(launch.hello, api.FeatureNamedSSHKeysV1); err != nil {
			return err
		}
	}
	args, err := json.Marshal(struct {
		ProtectedTrustedSecretValues map[string]api.Secret     `json:"protected_trusted_secret_values,omitempty"`
		Env                          map[string]api.Secret     `json:"env"`
		Members                      []api.MemberResumeArgs    `json:"members"`
		AllowMissing                 bool                      `json:"allow_missing"`
		SecretDelivery               *api.SecretDeliveryIntent `json:"secret_delivery,omitempty"`
		NamedSSHKeys                 *api.NamedSSHKeysV1       `json:"named_ssh_keys,omitempty"`
	}{protected, env, members, allowMissing, delivery, namedKeys})
	if err != nil {
		return err
	}
	defer clear(args)
	if err := launch.placement.gate(ctx, func() error {
		if err := launch.operation.native.check(ctx); err != nil {
			return err
		}
		if err := launch.checkNativeMemoryV1(ctx, launch.manifest); err != nil {
			return err
		}
		if err := launch.queueSandboxPrepareReplyV1(2); err != nil {
			return err
		}
		return launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 2, Op: "secrets", Args: args})
	}); err != nil {
		return err
	}
	_, err = launch.response(ctx, 2, true)
	return err
}

func (launch *startupLaunchV1) waitReady(ctx context.Context) (*Manifest, error) {
	launch.mu.Lock()
	defer launch.mu.Unlock()
	if !launch.loanSent || launch.line == nil || launch.ready {
		return nil, sessionLeaseError("readiness has no original pending startup connection")
	}
	if err := launch.queueSandboxPrepareReplyV1(3); err != nil {
		return nil, err
	}
	if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 3, Op: StartupReadyOperationV1}); err != nil {
		return nil, err
	}
	result, err := launch.response(ctx, 3, false)
	if err != nil {
		return nil, err
	}
	var ack struct {
		Nonce      string `json:"startup_nonce"`
		Generation string `json:"generation"`
	}
	if !api.ExactJSONFields(result, ack) || json.Unmarshal(result, &ack) != nil || ack.Nonce != launch.hello.StartupNonce ||
		len(ack.Generation) != 32 || !supply.IsHex64(ack.Generation+ack.Generation) {
		return nil, sessionLeaseError("the original readiness acknowledgement differs from its native startup")
	}
	m, err := launch.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if m.Life == nil || m.Life.Started.IsZero() || m.Run.Generation != ack.Generation || m.Run.HolderPID != launch.nativePeer().pid ||
		!validOriginalReadyNeed(launch.manifest, m) || !reflect.DeepEqual(m.RuntimeManifest, launch.manifest.RuntimeManifest) {
		return nil, sessionLeaseError("the durable ready record differs from this original native supervisor")
	}
	if err := launch.checkNativeMemoryV1(ctx, m); err != nil {
		return nil, err
	}
	launch.ready = true
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := launch.queueSandboxPrepareReplyV1(5); err != nil {
		return nil, err
	}
	if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 5, Op: PolicyNextCommitOperationV1}); err != nil {
		return nil, err
	}
	if _, err := launch.response(ctx, 5, true); err != nil {
		return nil, err
	}
	return launch.operation.LoadContext(ctx)
}

func (launch *startupLaunchV1) cancelPending() error {
	if launch.line == nil {
		return nil
	}
	if launch.readFailed {
		return launch.line.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = launch.line.SetDeadline(time.Now().Add(5 * time.Second))
	if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 4, Op: cancelPendingStartOp}); err != nil {
		return errors.Join(err, launch.line.Close())
	}
	_, err := launch.response(ctx, 4, false)
	if err == nil {
		launch.cleanupConfirmed = true
	}
	return errors.Join(err, launch.line.Close())
}

func validOriginalReadyNeed(before, after *Manifest) bool {
	if bytes.Equal(before.VMNeedV1, after.VMNeedV1) {
		return true
	}
	if len(before.AutomaticRestore) == 0 {
		return false
	}
	original, err := vmneed.DecodeV1(before.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return false
	}
	current, err := vmneed.DecodeV1(after.VMNeedV1, runtime.GOARCH)
	if err != nil || after.Agents != current.Agents.SHA256 || current.ValidateWall(runtime.GOARCH, wallLevel(after), wallDriver(after), after.Limits.MemoryMax) != nil {
		return false
	}
	current.Agents = original.Agents
	return reflect.DeepEqual(*original, *current)
}
