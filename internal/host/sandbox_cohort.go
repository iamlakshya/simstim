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
func SandboxCohortSnapshot(ctx context.Context, original string) (*api.SandboxCohortView, error) {
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
	raw, err := holderRequest(operation.Primary(), "info", map[string]bool{"cohort": true}, 5*time.Second, func(hello proto.Hello) error {
		return requireSupervisorFeatures(hello, api.FeatureSandboxCohort, api.FeatureBoundSecrets)
	})
	if err != nil {
		return nil, err
	}
	var current Manifest
	if json.Unmarshal(raw, &current) != nil || current.ID != before.ID || current.SandboxOf != "" || current.Run.Generation != before.Run.Generation || current.Run.BootID != before.Run.BootID || !current.Run.StartedAt.Equal(before.Run.StartedAt) || current.SecretState == nil {
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
	view := &api.SandboxCohortView{Snapshot: snapshot, SecretState: api.CloneSessionSecretState(current.SecretState), RunGeneration: before.Run.Generation}
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
