package egress

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func trustedRoleArgs(t *testing.T, guest bool) BoundInitArgs {
	t.Helper()
	use := api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: api.SecretBind{Hosts: []string{"judge.example:443"}, Header: "Authorization", Format: "Bearer {value}"}, Model: "local-model", Endpoint: "https://judge.example/v1/chat/completions"}
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: new("locked")}, Classifier: api.ClassifierValues{Judge: new("model"), Key: new("KEY"), Model: new(use.Model), Endpoint: new(use.Endpoint)}}}, {Kind: api.LayerSession}})
	if err != nil {
		t.Fatal(err)
	}
	args := BoundInitArgs{Version: MemberRegistryV2, Initial: InitArgs{SessionID: "trusted2", Agent: "shell", Policy: policy}, TrustedUses: map[string]api.TrustedSecretUseV1{"KEY": use}, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "ABCD0000"}}
	if guest {
		args.Modes = map[string]api.SecretMode{"KEY": api.SecretModeBound}
		args.Binds = map[string]api.SecretBind{"KEY": use.Bind}
		args.Initial.Secrets = map[string]string{"KEY": "ABCD0000"}
		args.SourceKinds = map[string][]string{"KEY": {""}}
	}
	return args
}

func TestTrustedJudgeOnlyUsesTheSameRegistryWithoutGuestProjection(t *testing.T) {
	args := trustedRoleArgs(t, false)
	server, result := newBoundRegistryServer(t, args)
	secret := server.currentMembers().secrets["KEY"]
	if len(server.currentMembers().secrets) != 1 || secret.trusted == nil || !secret.trustedArmed || !secret.present || secretHasGuestUse(secret) || secret.bound != nil || len(result.Secrets) != 0 || !EqualTrustedSecretUsesV1(result.TrustedUses, args.TrustedUses) {
		t.Fatal("judge-only delivery created a guest use or a second value store")
	}
	if len(server.currentMembers().requestEntries) != 0 || server.currentEngine().Decide(Request{Host: "judge.example", Port: 443}).Allow {
		t.Fatal("judge reference created a guest injector or network grant")
	}
	public, err := server.currentMembers().publicSessionProjection(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if len(public.Intent.Modes) != 0 || len(public.Primary) != 0 || !EqualTrustedSecretUsesV1(public.Intent.TrustedUses, args.TrustedUses) || strings.Contains(string(raw), "ABCD0000") {
		t.Fatal("public role metadata included value or guest sentinel state")
	}
	if err := ValidateBoundInitialResult(t.Context(), args, result); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedAndGuestRolesShareOneValueAndRotateOnlyWithSeparateApproval(t *testing.T) {
	args := trustedRoleArgs(t, true)
	server, initial := newBoundRegistryServer(t, args)
	before := server.currentMembers()
	if len(before.secrets) != 1 || !secretHasGuestUse(before.secrets["KEY"]) || before.secrets["KEY"].trusted == nil || !before.secrets["KEY"].trustedArmed {
		t.Fatal("explicit duplicate guest use was dropped")
	}
	// Guest-only rotation keeps its live stand-in and makes the trusted use
	// unavailable until that role's own original intake is authorised again.
	result, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil {
		t.Fatal(err)
	}
	current := server.currentMembers()
	if current.secrets["KEY"].trustedArmed || result.Secrets["KEY"].Sentinel != initial.Secrets["KEY"].Sentinel {
		t.Fatal("guest rotation donated trusted arming or changed guest stand-in")
	}
	result, err = server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: result.Generation, TrustedUses: args.TrustedUses, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !server.currentMembers().secrets["KEY"].trustedArmed || len(result.Secrets) != 0 {
		t.Fatal("same-value trusted arm injected a guest environment update")
	}
	current = server.currentMembers()
	_, err = server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: result.Generation, TrustedUses: args.TrustedUses, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "JKLM2222"}}))
	if err == nil || server.currentMembers() != current {
		t.Fatal("trusted-only rotation changed an independently authorised guest value")
	}
}

func TestTrustedRoleRejectsMismatchedGuestValueOrEndpointBeforePublication(t *testing.T) {
	for _, alter := range []func(*BoundInitArgs){
		func(args *BoundInitArgs) { args.ProtectedTrustedSecretValues["KEY"] = "WXYZ1111" },
		func(args *BoundInitArgs) {
			use := args.TrustedUses["KEY"]
			use.Endpoint = "https://other.example/v1/chat/completions"
			args.TrustedUses["KEY"] = use
		},
		func(args *BoundInitArgs) { args.ProtectedTrustedSecretValues["EXTRA"] = "WXYZ1111" },
	} {
		args := trustedRoleArgs(t, true)
		alter(&args)
		server := uninitialisedMemberServer(t)
		before := server.currentEngine()
		if _, err := server.initialiseBoundControl(memberJSON(t, args)); err == nil || server.isInited() || server.currentMembers() != nil || server.currentEngine() != before {
			t.Fatal("mismatched protected role partially published")
		}
	}
}

func TestColdTrustedRoleStaysUnarmedAndClonesItsBinding(t *testing.T) {
	args := trustedRoleArgs(t, false)
	args.ProtectedTrustedSecretValues = nil
	server, _ := newBoundRegistryServer(t, args)
	secret := server.currentMembers().secrets["KEY"]
	if secret.present || secret.trustedArmed || secret.value != "" || secret.bound != nil {
		t.Fatal("cold role reconstructed a value or stand-in")
	}
	clone, err := cloneMemberState(server.currentMembers())
	if err != nil {
		t.Fatal(err)
	}
	clone.secrets["KEY"].trusted.Bind.Hosts[0] = "changed.example:443"
	if server.currentMembers().secrets["KEY"].trusted.Bind.Hosts[0] != "judge.example:443" {
		t.Fatal("cloned role borrowed original binding authority")
	}
}

func TestTrustedOnlyResponseEchoIsRefusedWithoutFabricatingASentinel(t *testing.T) {
	args := trustedRoleArgs(t, false)
	server, _ := newBoundRegistryServer(t, args)
	reader, err := newResponseCredentialReader(t.Context(), io.NopCloser(strings.NewReader("ABCD0000")), func(ctx context.Context) (*responseEchoSnapshot, error) {
		return server.responseSecretSnapshot(ctx, "judge.example:443", secretRequestUse{})
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	output, err := io.ReadAll(reader)
	if err == nil || strings.Contains(string(output), "ABCD") || len(output) != 0 {
		t.Fatal("trusted-only echo was released or replaced with a fabricated guest stand-in")
	}
}

func TestExplicitMemberCanAddGuestRoleToAnExistingTrustedName(t *testing.T) {
	args := trustedRoleArgs(t, false)
	server, initial := newBoundRegistryServer(t, args)
	result, err := server.addBoundControlMember(memberJSON(t, MemberAddV2Args{Version: 2, Generation: initial.Generation, Kind: "shell", Values: map[string]api.Secret{"KEY": "ABCD0000"}, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": args.TrustedUses["KEY"].Bind}}))
	if err != nil {
		t.Fatal(err)
	}
	current := server.currentMembers().secrets["KEY"]
	if !current.guest || current.trusted == nil || !current.trustedArmed || current.bound == nil || result.Env["KEY"] == "ABCD0000" || len(result.Secrets) != 1 {
		t.Fatal("explicit guest addition lost role isolation")
	}
}
