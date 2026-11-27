package host

import (
	"context"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

// PullSourceOpener is implemented by the canonical sandbox resource owner.
// It holds one primary operation lease from before its fresh manifest read
// until Source.Close, including full retries and RecordPulled. It never wakes
// the host, starts a session, sends a secret or publishes clean file state.
type PullSourceOpener func(ctx context.Context, originalID, expectedPrimary string) (pullexport.Source, error)

var pullSource struct {
	sync.RWMutex
	open PullSourceOpener
}

// RegisterPullSource installs the actual canonical owner at helper startup.
// Registration happens before any bridge request or sandbox launch.
func RegisterPullSource(open PullSourceOpener) {
	pullSource.Lock()
	defer pullSource.Unlock()
	pullSource.open = open
}

func openPullSource(ctx context.Context, original, primary string) (pullexport.Source, error) {
	pullSource.RLock()
	open := pullSource.open
	pullSource.RUnlock()
	if open == nil {
		return nil, UserError("pull_owner", "the helper has no canonical bundle resource owner", "Reinstall the complete host helper, then pull again.")
	}
	return open(ctx, original, primary)
}

// EffectivePullMax reads only the protected policy already applied to a
// sandbox. An explicit empty or invalid value refuses instead of refilling.
func EffectivePullMax(m *Manifest) (int64, error) {
	if m == nil {
		return 0, UserError("pull_policy", "the pull has no protected session policy", "Check the session manifest before pulling.")
	}
	if m.Policy == nil || m.Policy.Sandbox.PullMax == nil {
		return api.PullMaxDefault, nil
	}
	limit, err := api.ParseSize(*m.Policy.Sandbox.PullMax)
	if err != nil || limit <= 0 {
		return 0, UserError("pull_policy", "the applied sandbox.pull_max is invalid", "Set a positive size in the sandbox policy, then pull again.")
	}
	return limit, nil
}

// ProtectedSeedHeads returns only identities saved by the seed authority.
// The FilePlan fallback supports P16 records written before the top-level
// history map. Both fields are protected host data, never guest claims.
func ProtectedSeedHeads(m *Manifest) ([]string, error) {
	var heads []string
	for _, oid := range m.SeedHeads {
		heads = append(heads, oid)
	}
	if len(heads) == 0 && m.Files != nil && m.Files.Seed.Commit != "" {
		heads = append(heads, m.Files.Seed.Commit)
	}
	slices.Sort(heads)
	heads = slices.Compact(heads)
	for _, oid := range heads {
		if !api.ValidGitOID(oid) {
			return nil, UserError("pull_seed", "the protected seed history is invalid", "Check the seed record before pulling.")
		}
	}
	return heads, nil
}
