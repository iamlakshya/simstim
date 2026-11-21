//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type slotReservedStartupSourceV2 interface {
	WithReservedStartupFilesV2(context.Context, *SessionOperation, *ColdResumeV1, *startupLaunchV1, func([]*os.File) error) error
}

// The caller holds launch.mu on the exact native creation. Ordinary OP1 is
// unchanged; this closed purpose sends OP plus the genuine reserved18 only.
func (launch *startupLaunchV1) sendSlotReservedStartupV2Locked(ctx context.Context, m *Manifest, base startupLoanV1, p *bridgeRewindPreparationV1) (returned error) {
	if p == nil || p.operation != launch.operation || p.cold == nil || p.operation.nativeColdResume != p.cold || p.cold.startup != launch || p.slotSnapshot == nil {
		return rewindFailureV1("the reserved startup has no original closed rewind and actual new creation")
	}
	if err := launch.checkReservedStartupPurposeV2(m, base); err != nil {
		return err
	}
	if err := p.checkCommittedV1(ctx, p.operation, p.cold, p.fresh); err != nil {
		return err
	}
	if err := requireSupervisorFeatures(launch.hello, proto.FeatureNativeSlotReservedStartupV2); err != nil {
		return err
	}
	source, ok := p.slotSnapshot.(slotReservedStartupSourceV2)
	if !ok {
		return rewindFailureV1("the actual slot owner has no creation-bound reserved startup producer")
	}
	prepare := slotReservedStartupPrepareV2{Version: 2, Primary: m.ID, Nonce: base.Nonce, Need: base.Need, Runtime: base.Runtime}
	raw, err := json.Marshal(prepare)
	if err != nil {
		return err
	}
	if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: proto.NativeSlotReservedStartupPrepareRequestV2, Op: proto.NativeSlotReservedStartupPrepareV2, Args: raw}); err != nil {
		return err
	}
	ackRaw, err := launch.response(ctx, proto.NativeSlotReservedStartupPrepareRequestV2, true)
	if err != nil {
		return err
	}
	var prepared struct {
		Version int    `json:"v"`
		Nonce   string `json:"startup_nonce"`
	}
	if !api.ExactJSONFields(ackRaw, prepared) || json.Unmarshal(ackRaw, &prepared) != nil || prepared.Version != 2 || prepared.Nonce != base.Nonce {
		return rewindFailureV1("the original new supervisor did not prepare its exact reserved startup reader")
	}
	operation, err := launch.operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	loanRaw, err := json.Marshal(slotReservedStartupLoanV2{Version: 2, Base: base})
	if err != nil {
		return err
	}
	err = source.WithReservedStartupFilesV2(ctx, launch.operation, p.cold, launch, func(reserved []*os.File) error {
		if len(reserved) != proto.NativeCheckpointReservationRoleCountV2 {
			return rewindFailureV1("the original reserved startup source supplied another exact role layout")
		}
		files := append([]*os.File{operation}, reserved...)
		return launch.placement.gateControl(ctx, func(control *placementControl) error {
			if err := launch.operation.native.check(ctx); err != nil {
				return err
			}
			if err := launch.checkNativeMemoryV1(ctx, m); err != nil {
				return err
			}
			if err := checkColdNativeTakeoverLockedV1(ctx, launch.operation); err != nil {
				return err
			}
			if err := takeoverResumePreparationNativeLockedV1(ctx, launch.operation, p.cold, control); err != nil {
				return err
			}
			launch.loanSent = true // uncertain transfer keeps the actual creation/OP
			return launch.line.writeSlotReservedStartupV2(proto.Request{ID: proto.NativeSlotReservedStartupLoanRequestV2, Op: proto.NativeSlotReservedStartupLoanV2, Args: loanRaw}, files)
		})
	})
	if err != nil {
		return err
	}
	result, err := launch.response(ctx, proto.NativeSlotReservedStartupLoanRequestV2, true)
	if err != nil {
		return err
	}
	var ack struct {
		Nonce string `json:"startup_nonce"`
	}
	if !api.ExactJSONFields(result, ack) || json.Unmarshal(result, &ack) != nil || ack.Nonce != base.Nonce {
		return rewindFailureV1("the original reserved startup did not acknowledge its exact borrowed operation")
	}
	return launch.check(ctx)
}

// The common original sender has already consumed a real automatic intent
// before calling this method. Keep that exact provenance on this distinct
// native role class; a purpose string cannot introduce an automatic grant.
func (launch *startupLaunchV1) checkReservedStartupPurposeV2(m *Manifest, base startupLoanV1) error {
	if m == nil || launch.operation == nil {
		return rewindFailureV1("the reserved startup lost its original purpose producer")
	}
	if base.Purpose == "ordinary" && base.Automatic == "" && len(m.AutomaticRestore) == 0 {
		return nil
	}
	if base.Purpose != "automatic" || base.Automatic == "" || base.Automatic != startupDigest(m.AutomaticRestore) {
		return automaticRestoreError()
	}
	boot := launch.operation.AutomaticBoot()
	if boot == nil {
		return automaticRestoreError()
	}
	boot.mu.Lock()
	defer boot.mu.Unlock()
	if !boot.used || boot.operation != launch.operation || !bytes.Equal(boot.plan, m.AutomaticRestore) {
		return automaticRestoreError()
	}
	_, err := ValidateAutomaticRestore(m)
	return err
}
