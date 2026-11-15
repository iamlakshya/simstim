package client

import (
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNewInputDeclarationCannotOutliveOriginalSettingsFD(t *testing.T) {
	isolateTypedSecretSettings(t)
	if err := os.WriteFile(settingsPath(), []byte("[sandbox]\nenv={MODE='original'}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	nonce, err := api.NewInputNonce()
	if err != nil {
		t.Fatal(err)
	}
	p := &newInputPreparation{operation: operation, payload: true, ready: true, plan: &SessionInputPlan{
		summary: api.InputSummary{Version: api.SessionInputsV1, Nonce: nonce, Digest: strings.Repeat("a", 64)},
		env:     map[string]string{"MODE": "original"}}}
	args, err := p.inputArguments(t.Context(), NewArgs{Wall: "process"})
	if err != nil || args.Inputs == nil || args.Environment == nil || args.Environment.Values["MODE"] != "original" {
		t.Fatal("original source did not supply its complete input/environment declaration", err)
	}
	args.Environment.Values["MODE"] = "caller-changed"
	if _, err := p.inputArguments(t.Context(), args); err == nil {
		t.Fatal("caller environment changed the sealed declaration")
	}
	if err := os.WriteFile(settingsPath(), []byte("[sandbox]\nenv={MODE='replacement'}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err = p.inputArguments(t.Context(), NewArgs{Wall: "process"})
	if err == nil || args.Inputs != nil || args.Environment != nil {
		t.Fatal("replaced original settings still published an input/environment declaration", err)
	}
}

func TestBindNewWallKeepsOriginalRequestAndRefusesScopeDonation(t *testing.T) {
	isolateTypedSecretSettings(t)
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.BindNewWall(ctx, "process", []string{"KEY"}); err != nil {
		t.Fatal(err)
	}
	if r.preparationSettings != operation || grantRequestOf(ctx) != r {
		t.Fatal("resolved wall replaced the original retained operation")
	}
	if err := r.BindNewWall(ctx, "process", []string{"KEY"}); err != nil {
		t.Fatal("same original wall retry failed", err)
	}
	for _, expose := range [][]string{nil, {"OTHER"}} {
		if err := r.BindNewWall(ctx, "process", expose); err == nil {
			t.Fatal("a changed exposure tuple donated the retained settings operation")
		}
	}
	answered := NewGrantRequest(false, false)
	answered.confirmed = map[grantScope]bool{{kind: OutboundSecret, name: "KEY"}: true}
	if err := answered.BindNewWall(WithGrantRequest(t.Context(), answered), "process", []string{"KEY"}); err == nil {
		t.Fatal("earlier unrelated terminal answer donated wall intent")
	}
}
