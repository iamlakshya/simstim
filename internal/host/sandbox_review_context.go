package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SavedSandboxReviewContext reads the last actual agent publication under the
// SAME primary operation. It never opens a guest, wakes a sandbox, runs Store
// or turns saved names into local source authority. H5 restricts its receipt.
func SavedSandboxReviewContext(ctx context.Context, original string) (*api.SandboxCohortView, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if !api.ValidSessionID(original) {
		return nil, savedReviewUnavailable()
	}
	operation, err := BeginSessionOperation(ctx, original, supervisorSnapshotWait)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	before, err := operation.Load()
	if err != nil || before == nil || before.ID != operation.Primary() || before.SandboxOf != "" || SessionRunning(before) || before.SecretState == nil || !publicationRunCoherent(before, nil) {
		return nil, savedReviewUnavailable()
	}
	selected := before
	if original != before.ID {
		selected, err = LoadManifest(original)
		if err != nil || !publicationRunCoherent(selected, []*Manifest{before}) {
			return nil, savedReviewUnavailable()
		}
	}
	children, err := SandboxMembers(before.ID)
	if err != nil {
		return nil, err
	}
	snapshot, err := snapshotFromProtected(before, children, original)
	if err != nil {
		return nil, err
	}
	state := api.CloneSessionSecretState(before.SecretState)
	if err := egress.ValidateSavedPublicSessionState(state); err != nil {
		return nil, savedReviewUnavailable()
	}
	after, err := operation.Load()
	if err != nil || SessionRunning(after) || !sameSavedPublication(before, after) {
		return nil, savedReviewUnavailable()
	}
	if original != before.ID {
		current, err := LoadManifest(original)
		if err != nil || !sameSavedPublication(selected, current) || !publicationRunCoherent(current, []*Manifest{after}) {
			return nil, savedReviewUnavailable()
		}
	}
	if err := CheckSandboxSnapshotLocked(original, before.ID, snapshot); err != nil {
		return nil, err
	}
	view := &api.SandboxCohortView{Snapshot: snapshot, SecretState: state, RunGeneration: before.Run.Generation}
	payload, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	if err := proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: payload}); err != nil {
		return nil, UserError("sandbox_size", "the saved sandbox review context exceeds its whole response bound", "Reduce retained public metadata before requesting its saved context again.")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return view.Clone(), nil
}
func sameSavedPublication(a, b *Manifest) bool {
	if a == nil || b == nil || a.ID != b.ID || a.SandboxOf != b.SandboxOf || a.Run.Generation != b.Run.Generation || a.Run.BootID != b.Run.BootID || !a.Run.StartedAt.Equal(b.Run.StartedAt) || a.SecretState == nil || b.SecretState == nil {
		return false
	}
	left, e1 := json.Marshal(a.SecretState)
	right, e2 := json.Marshal(b.SecretState)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}
func savedReviewUnavailable() error {
	return UserError("saved_context_unavailable", "the saved agent publication context is missing, unsupported or changed", "Keep the original local receipt. Explicitly resume with a matching helper before creating a new publication receipt.")
}
