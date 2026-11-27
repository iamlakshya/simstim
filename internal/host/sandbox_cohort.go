package host

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SandboxCohortSnapshot reads the live root cohort and structural snapshot
// under ONE canonical primary operation. It neither starts nor wakes a guest.
func SandboxCohortSnapshot(ctx context.Context, original string, supplied ...*api.TrustedSecretReadinessQueryV1) (*api.SandboxCohortView, error) {
	if len(supplied) > 1 {
		return nil, snapshotChanged()
	}
	var query *api.TrustedSecretReadinessQueryV1
	if len(supplied) == 1 {
		query = api.CloneTrustedSecretReadinessQueryV1(supplied[0])
	}
	operation, err := BeginSessionOperation(ctx, original, supervisorSnapshotWait)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	before, err := operation.Load()
	if err != nil {
		return nil, err
	}
	if !SessionRunning(before) || before.Run.Generation == "" {
		return nil, snapshotChanged()
	}
	snapshot, err := SandboxSnapshotLocked(original, operation.Primary())
	if err != nil {
		return nil, err
	}
	raw, err := holderRequest(operation.Primary(), "info", api.SandboxCohortInfoArgsV1{Cohort: true, TrustedReadiness: query}, 5*time.Second, func(hello proto.Hello) error {
		features := []string{api.FeatureSandboxCohort}
		if IsNoneSupervisor(before) {
			features = append(features, proto.FeatureNoneSupervisorV1)
		} else {
			features = append(features, api.FeatureBoundSecrets)
		}
		if query != nil {
			features = append(features, api.FeatureJudgeKeyV1)
		}
		return requireSupervisorFeatures(hello, features...)
	})
	if err != nil {
		return nil, err
	}
	var readiness *api.TrustedSecretReadinessV1
	if query != nil {
		var result api.SandboxCohortInfoResultV1
		fields, err := api.JSONObjectFields(raw, "current", "trusted_readiness")
		if err != nil || len(fields) != 2 || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || len(result.Current) == 0 || result.TrustedReadiness == nil || !result.TrustedReadiness.ValidFor(*query) {
			return nil, snapshotChanged()
		}
		readiness = api.CloneTrustedSecretReadinessV1(result.TrustedReadiness)
		raw = result.Current
	}
	var current Manifest
	if json.Unmarshal(raw, &current) != nil || current.ID != before.ID || current.SandboxOf != "" || current.Run.Generation != before.Run.Generation || current.Run.BootID != before.Run.BootID || !current.Run.StartedAt.Equal(before.Run.StartedAt) || current.SecretState == nil {
		return nil, snapshotChanged()
	}
	if readiness != nil && current.SecretState.Generation != readiness.Generation {
		return nil, snapshotChanged()
	}
	if err := egress.ValidateSavedPublicSessionState(current.SecretState); err != nil {
		return nil, err
	}
	after, err := operation.Load()
	if err != nil {
		return nil, err
	}
	if after.Run.Generation != before.Run.Generation || after.Run.BootID != before.Run.BootID || !after.Run.StartedAt.Equal(before.Run.StartedAt) {
		return nil, snapshotChanged()
	}
	if err := CheckSandboxSnapshotLocked(original, operation.Primary(), snapshot); err != nil {
		return nil, err
	}
	view := &api.SandboxCohortView{TrustedReadiness: readiness, Snapshot: snapshot, SecretState: api.CloneSessionSecretState(current.SecretState), RunGeneration: before.Run.Generation}
	// Count the actual complete outer response, using the largest request ID.
	result, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	whole, err := json.Marshal(proto.Response{ID: math.MaxUint64, OK: true, Result: result})
	if err != nil || len(whole)+1 > proto.MaxFrame {
		return nil, UserError("sandbox_size", "the complete sandbox cohort exceeds its response bound", "Reduce the sandbox's retained public metadata before adding an agent.")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return view, nil
}
