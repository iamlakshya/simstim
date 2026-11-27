package host

import (
	"cmp"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type restoreCandidateV1 struct {
	id       string
	activity time.Time
}

// Restore metadata orders requests only. restoreOneContext reloads each protected
// primary under its original operation before any native start or publication.
func restoreActivityV1(id string) time.Time {
	m, err := LoadManifest(id)
	if err != nil || m.SandboxOf != "" {
		return time.Time{}
	}
	activity := m.LastActivityAt
	members, err := SandboxMembers(id)
	if err != nil {
		return activity
	}
	for _, member := range members {
		if member.LastActivityAt.After(activity) {
			activity = member.LastActivityAt
		}
	}
	return activity
}

// Spec 6.8 admits at most two cold starts, with recent activity first. A pair
// joins before the next pair, so an older request cannot pass both newer ones.
func runRestoreCandidatesV1(candidates []restoreCandidateV1, restore func(string) (RestoreResult, bool)) []RestoreResult {
	slices.SortFunc(candidates, func(a, b restoreCandidateV1) int {
		if order := b.activity.Compare(a.activity); order != 0 {
			return order
		}
		return cmp.Compare(a.id, b.id)
	})
	type result struct {
		row RestoreResult
		ok  bool
	}
	results := make([]result, len(candidates))
	for first := 0; first < len(candidates); first += 2 {
		var workers sync.WaitGroup
		for index := first; index < min(first+2, len(candidates)); index++ {
			workers.Go(func() { results[index].row, results[index].ok = restore(candidates[index].id) })
		}
		workers.Wait()
	}
	var out []RestoreResult
	for _, result := range results {
		if result.ok {
			out = append(out, result.row)
		}
	}
	return out
}

func sandboxWasWorkingV1(primary *Manifest) (bool, error) {
	members, err := SandboxMembers(primary.ID)
	if err != nil {
		return false, err
	}
	for _, member := range append([]*Manifest{primary}, members...) {
		if !member.Ended() && member.Guest != nil && member.Guest.State == StateWorking {
			return true, nil
		}
	}
	return false, nil
}

func lazyRestoreDecisionV1(operation *SessionOperation, primary *Manifest, all bool, selection *AutomaticRestoreSelection) (RestoreResult, bool) {
	if all || selection == nil {
		return RestoreResult{}, false
	}
	working, err := sandboxWasWorkingV1(primary)
	if err != nil {
		return lazyRestoreFailureV1(err), true
	}
	if working {
		return RestoreResult{}, false
	}
	if err := stopLazyRestoreV1(operation); err != nil {
		return lazyRestoreFailureV1(err), true
	}
	return RestoreResult{Action: "stopped", Why: "the sandbox was idle; attach to wake it"}, true
}

func lazyRestoreFailureV1(err error) RestoreResult {
	result := RestoreResult{Action: "failed", Why: err.Error(), Hint: "Check the host with host doctor, then retry restore."}
	var failure *proto.Error
	if errors.As(err, &failure) && failure.Hint != "" {
		result.Hint = failure.Hint
	}
	return result
}
