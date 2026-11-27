package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func savedSecretFixture() (host.Manifest, *host.Manifest) {
	current, _ := validPublicMintFixture()
	root := host.Manifest{ID: "root2222", Secrets: []string{"CREDENTIAL"}, SecretState: current}
	old := api.CloneSessionSecretState(current)
	public := old.Primary["CREDENTIAL"]
	public.Sentinel, public.Identity = api.Secret("OLDX1111"), strings.Repeat("f", 32)
	old.Primary["CREDENTIAL"] = public
	old.Generation = strings.Repeat("d", 32)
	child := &host.Manifest{ID: "child222", SandboxOf: root.ID, Secrets: []string{"CREDENTIAL"}, SecretState: old}
	return root, child
}

func TestSavedRootWinsWhileOldChildRemainsExplicitRetiredTripwire(t *testing.T) {
	root, child := savedSecretFixture()
	merged, err := collectSavedSecretState(context.Background(), root, []*host.Manifest{child})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Primary["CREDENTIAL"].Sentinel != root.SecretState.Primary["CREDENTIAL"].Sentinel || merged.Primary["CREDENTIAL"].Armed || len(merged.Retired["CREDENTIAL"]) != 1 || merged.Retired["CREDENTIAL"][0].Sentinel != child.SecretState.Primary["CREDENTIAL"].Sentinel || len(merged.Minted) != 0 {
		t.Fatal("old child displaced canonical root, stayed armed, or became fake minted data")
	}
	root.SecretState = merged
	again, err := collectSavedSecretState(context.Background(), root, []*host.Manifest{child})
	if err != nil || len(again.Retired["CREDENTIAL"]) != 1 {
		t.Fatal("repeated collection grew duplicate public history")
	}
}

func TestSavedMemberOnlyNamespaceSurvivesWithoutPhysicalPrimaryLaunch(t *testing.T) {
	root, child := savedSecretFixture()
	root.SecretState, root.Secrets = nil, nil
	merged, err := collectSavedSecretState(context.Background(), root, []*host.Manifest{child})
	if err != nil || merged == nil || merged.Primary["CREDENTIAL"].Sentinel != child.SecretState.Primary["CREDENTIAL"].Sentinel || merged.Primary["CREDENTIAL"].Armed {
		t.Fatal("member-only saved name was dropped or armed without a fresh value")
	}
	if root.SecretState != nil {
		t.Fatal("pure saved collector modified controller selection or root state")
	}
}

func TestSavedCollectorRejectsForeignFutureAndUnrecordedNames(t *testing.T) {
	for _, mutation := range []string{"foreign", "future", "unrecorded"} {
		t.Run(mutation, func(t *testing.T) {
			root, child := savedSecretFixture()
			switch mutation {
			case "foreign":
				child.SandboxOf = "other222"
			case "future":
				child.SecretState.Version++
			case "unrecorded":
				child.Secrets = nil
			}
			if _, err := collectSavedSecretState(context.Background(), root, []*host.Manifest{child}); err == nil {
				t.Fatal("unproven saved descriptor entered the startup namespace")
			}
		})
	}
}

func TestTwoChildrenCannotInventCurrentWinnerWithoutRootRecord(t *testing.T) {
	root, first := savedSecretFixture()
	second := host.CloneManifest(*first)
	second.ID = "other222"
	public := second.SecretState.Primary["CREDENTIAL"]
	public.Sentinel, public.Identity = api.Secret("NEXT2222"), strings.Repeat("e", 32)
	second.SecretState.Primary["CREDENTIAL"] = public
	root.SecretState, root.Secrets = nil, nil
	if _, err := collectSavedSecretState(context.Background(), root, []*host.Manifest{first, &second}); err == nil {
		t.Fatal("ambiguous children selected a primary by traversal order")
	}
}
