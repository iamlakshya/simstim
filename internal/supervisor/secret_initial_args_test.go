package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestFreshNextStartRetiresOldShapeAndAllowsNewLength(t *testing.T) {
	retained, _ := validPublicMintFixture()
	old := retained.Primary["CREDENTIAL"]
	fresh := &startupSecretInput{Intent: api.CloneSecretDeliveryIntent(retained.Intent), Values: map[string]api.Secret{"CREDENTIAL": api.Secret("LONGERVALUE123456")}, SourceKinds: map[string][]string{"CREDENTIAL": {""}}}
	args, err := boundStartupArgs(context.Background(), egress.InitArgs{Agent: "custom-command"}, retained, fresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, constrained := args.Saved["CREDENTIAL"]; constrained || args.Initial.Secrets["CREDENTIAL"] != string(fresh.Values["CREDENTIAL"]) || len(args.SavedRetired["CREDENTIAL"]) != 1 || args.SavedRetired["CREDENTIAL"][0].Sentinel != old.Sentinel {
		t.Fatal("fresh next-start was forced back to old length or lost its tripwire")
	}
	if retained.Primary["CREDENTIAL"].Sentinel != old.Sentinel || len(retained.Retired) != 0 {
		t.Fatal("prospective next-start changed existing saved state")
	}
}

func TestAutomaticColdArgsKeepShapeAndNeverFabricateFreshSources(t *testing.T) {
	retained, _ := validPublicMintFixture()
	args, err := boundStartupArgs(context.Background(), egress.InitArgs{}, retained, nil, &retained.Intent)
	if err != nil || args.Initial.Secrets != nil || args.SourceKinds != nil || args.Saved["CREDENTIAL"].Sentinel != retained.Primary["CREDENTIAL"].Sentinel {
		t.Fatal("automatic restore fetched, inferred or dropped saved format")
	}
	public := args.Saved["CREDENTIAL"]
	public.Shape.Slots[0].Count = 99
	if retained.Primary["CREDENTIAL"].Shape.Slots[0].Count == 99 {
		t.Fatal("initial Saved borrows the protected record's mutable shape")
	}
}

func TestMissingNewBoundFormatAndUnauthorisedModeFlipRefuse(t *testing.T) {
	intent := boundStartupIntent("CREDENTIAL")
	fresh := &startupSecretInput{Intent: *intent, Values: map[string]api.Secret{}}
	if _, err := boundStartupArgs(context.Background(), egress.InitArgs{}, nil, fresh, nil); err == nil {
		t.Fatal("missing fresh bound name fabricated a format or exposed a value")
	}
	retained, _ := validPublicMintFixture()
	fresh.Intent = api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"CREDENTIAL": api.SecretModeExposed}}
	fresh.Values["CREDENTIAL"] = api.Secret("private-input-marker")
	_, err := boundStartupArgs(context.Background(), egress.InitArgs{}, retained, fresh, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "secret_intent" || !strings.Contains(pe.Hint, "new sandbox") ||
		strings.Contains(pe.Message, "private-input-marker") || strings.Contains(pe.Hint, "private-input-marker") {
		t.Fatalf("public mismatch authorised exposure or gave an inaccurate recovery hint: %v", err)
	}
}
