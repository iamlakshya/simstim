package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundInitialReplyValidationUsesActualValuesSourcesAndCompleteDomains(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"A": "ABCD0000", "B": "ABCD0000"})
	_, result := newBoundRegistryServer(t, args)
	if err := ValidateBoundInitialResult(t.Context(), args, result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BoundInitResult){
		func(r *BoundInitResult) { delete(r.Secrets, "B") },
		func(r *BoundInitResult) { p := r.Secrets["A"]; p.Armed = false; r.Secrets["A"] = p },
		func(r *BoundInitResult) { p := r.Secrets["A"]; p.Sentinel = "ABCD0000"; r.Secrets["A"] = p },
		func(r *BoundInitResult) { p := r.Secrets["A"]; p.Mode = api.SecretModeExposed; r.Secrets["A"] = p },
		func(r *BoundInitResult) { p := r.Secrets["A"]; p.Shape.Slots[0].Count++; r.Secrets["A"] = p },
		func(r *BoundInitResult) {
			p := r.Secrets["A"]
			p.Identity = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			r.Secrets["A"] = p
		},
	} {
		candidate := result
		candidate.Secrets = api.ClonePublicSecretStates(result.Secrets)
		mutate(&candidate)
		if err := ValidateBoundInitialResult(t.Context(), args, candidate); err == nil {
			t.Fatal("inconsistent returned primary metadata passed the canonical producer barrier")
		}
	}
}

func TestBoundFreshNextStartCanDeriveNewShapeOnlyByExplicitlyRetiringOldCurrent(t *testing.T) {
	old := retiredPrimaryFixture("ABCD0000", 'a')
	args := boundRegistryArgs(map[string]string{"KEY": "ABCDE1111"})
	args.SavedRetired = map[string][]api.RetiredPrimarySecretState{"KEY": {old}}
	_, result := newBoundRegistryServer(t, args)
	if len(result.Secrets["KEY"].Sentinel) != 9 || len(result.Retired["KEY"]) != 1 || result.Retired["KEY"][0].Sentinel != old.Sentinel {
		t.Fatal("explicit fresh shape replacement discarded its old stand-in or reused live shape")
	}
	if err := ValidateBoundInitialResult(t.Context(), args, result); err != nil {
		t.Fatal(err)
	}
	strict := args
	strict.Saved = map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: old.Sentinel, Shape: old.Shape, Identity: old.Identity}}
	if _, err := prepareBoundInitial(t.Context(), strict); err == nil {
		t.Fatal("ordinary Saved mismatch inferred fresh replacement authority")
	}
}
