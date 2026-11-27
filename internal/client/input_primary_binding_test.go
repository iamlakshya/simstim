package client

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func TestLaptopPrimaryNeedsActualVerificationFeatureBeforeSourceWork(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", inputtransfer.Feature, inputtransfer.FinishFeature)
	r := NewGrantRequest(false, false)
	defer r.Close()
	_, err := c.PrepareNewInputPolicy(WithGrantRequest(t.Context(), r), NewArgs{Agent: "shell"}, NewInputOptions{
		Seeds: []string{filepath.Join(t.TempDir(), "not-a-source")},
	})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "helper_old" || r.preparationSettings != nil || r.newInputs != nil {
		t.Fatal("old input helper reached local source work or acquired a preparation", err)
	}
}

func TestActualPrimaryPolicyEntersSealedDigestAndRejectsChangedCarrier(t *testing.T) {
	isolateTypedSecretSettings(t)
	dir := laptopSeedFixture(t)
	name := filepath.Join("."+brand.Name, "sandbox.toml")
	pullFixtureCommit(t, dir, name, "[sandbox]\nwall='process'\n")
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	metadata := func(ctx context.Context) (LocalGitMetadataSource, error) {
		return newOrdinaryLocalGitMetadataSource(ctx, operation)
	}
	discovery, err := discoverSessionSeeds(t.Context(), dir, []string{dir}, false, metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer discovery.Close()
	policy, err := discovery.primaryPolicy(t.Context(), false, "", metadata)
	if err != nil || policy == nil {
		t.Fatal("missing selected primary policy", err)
	}
	plan, err := buildSessionInputPlan(t.Context(), inputPlanSelection{mode: inputPlanNew, originalCwd: dir, seeds: []string{dir},
		discovery: discovery, metadata: metadata, resolvedEnv: map[string]string{}, sourcePin: []byte(`{"original":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	summary, err := plan.Summary()
	if err != nil || len(summary.Repositories) != 1 || summary.Repositories[0].Policy == nil || summary.Repositories[0].Policy.Hash != policy.Hash ||
		summary.Repositories[0].Commit != discovery.seeds[0].policy.commit {
		t.Fatal("actual selected commit/policy did not enter the sealed declaration", err)
	}
	changed := summary.Repositories[0].Clone()
	*changed.Policy.Layer.Sandbox.Wall = "none"
	if err := plan.withSources(t.Context(), func(_ api.SessionInputs, sources inputtransfer.Sources) error {
		return sources.Repository(t.Context(), changed, io.Discard)
	}); err == nil {
		t.Fatal("caller policy mutation reused the original bundle source")
	}
	declaration := plan.plan.Canonical()
	declaration.Repositories[0] = changed
	changedDigest, err := declaration.Digest()
	if err != nil || changedDigest == summary.Digest {
		t.Fatal("selected primary policy was omitted from the manifest digest", err)
	}
	originalHash := summary.Repositories[0].Policy.Hash
	summary.Repositories[0].Policy.Hash = strings.Repeat("d", 64)
	again, err := plan.Summary()
	if err != nil || again.Repositories[0].Policy.Hash != originalHash || again.Digest != summary.Digest {
		t.Fatal("public summary shared mutable original policy data", err)
	}
}
