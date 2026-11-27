package supervisor

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func trustedControlIntent() api.SecretDeliveryIntent {
	return api.SecretDeliveryIntent{Version: 1, TrustedUses: map[string]api.TrustedSecretUseV1{"KEY": {Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: api.SecretBind{Hosts: []string{"judge.example:443"}, Header: "Authorization", Format: "Bearer {value}"}, Model: "local-model", Endpoint: "https://judge.example/v1/chat/completions"}}}
}

func TestProtectedTrustedControlNamesNeverExpandGuestIntent(t *testing.T) {
	intent := trustedControlIntent()
	values := map[string]api.Secret{"KEY": "ABCD0000"}
	if err := validateProtectedTrustedValues(intent.TrustedUses, values, true); err != nil {
		t.Fatal(err)
	}
	if len(intent.Modes) != 0 || len(intent.Binds) != 0 {
		t.Fatal("trusted role created guest names or binding authority")
	}
	if err := validateProtectedTrustedValues(intent.TrustedUses, map[string]api.Secret{"OTHER": "ABCD0000"}, true); err == nil {
		t.Fatal("unrelated protected name was accepted")
	}
	if err := validateProtectedTrustedValues(intent.TrustedUses, nil, true); err == nil {
		t.Fatal("a value-free role was treated as an explicit arm")
	}
	if err := validateProtectedTrustedValues(intent.TrustedUses, nil, false); err != nil {
		t.Fatal("cold public role metadata could not remain unarmed", err)
	}
}

func TestStartupTrustedControlKeepsProtectedValuesOutsideEveryGuestMap(t *testing.T) {
	intent := trustedControlIntent()
	use := intent.TrustedUses["KEY"]
	fresh := &startupSecretInput{Intent: intent, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "ABCD0000"}}
	initial := egress.InitArgs{SessionID: "trusted2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, LayerValues: api.LayerValues{Classifier: api.ClassifierValues{Judge: new("model"), Model: new(use.Model), Endpoint: new(use.Endpoint), Key: new("KEY")}}}}
	args, err := boundStartupArgs(t.Context(), initial, nil, fresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args.Initial.Secrets) != 0 || len(args.Modes) != 0 || len(args.Binds) != 0 || args.ProtectedTrustedSecretValues["KEY"] != "ABCD0000" || !egress.EqualTrustedSecretUsesV1(args.TrustedUses, intent.TrustedUses) {
		t.Fatal("trusted startup value entered guest state or lost original role")
	}
	args.ProtectedTrustedSecretValues["KEY"] = "changed"
	role := args.TrustedUses["KEY"]
	role.Bind.Hosts[0] = "changed.example:443"
	args.TrustedUses["KEY"] = role
	if fresh.ProtectedTrustedSecretValues["KEY"] != "ABCD0000" || fresh.Intent.TrustedUses["KEY"].Bind.Hosts[0] != "judge.example:443" {
		t.Fatal("protected startup packet borrowed caller-owned values or binding")
	}
}

func TestMemberTrustedAcknowledgementRequiresExactRoleAndNoGuestResult(t *testing.T) {
	intent := trustedControlIntent()
	current := &api.SessionSecretState{Intent: api.SecretDeliveryIntent{Version: 1}}
	args := egress.MemberAddV2Args{Version: 2, TrustedUses: intent.TrustedUses, ProtectedTrustedSecretValues: map[string]api.Secret{"KEY": "ABCD0000"}}
	reply := egress.MemberAddResultV2{Version: 2, TrustedUses: api.CloneTrustedSecretUses(intent.TrustedUses)}
	if err := validateMemberSecretReply(t.Context(), args, reply, current); err != nil {
		t.Fatal(err)
	}
	reply.Env = map[string]api.Secret{"KEY": "ABCD0000"}
	if err := validateMemberSecretReply(t.Context(), args, reply, current); err == nil {
		t.Fatal("judge-only acknowledgement returned a guest value")
	}
	reply.Env = nil
	role := reply.TrustedUses["KEY"]
	role.Endpoint = "https://other.example/v1/chat/completions"
	reply.TrustedUses["KEY"] = role
	if err := validateMemberSecretReply(t.Context(), args, reply, current); err == nil {
		t.Fatal("member ACK changed original trusted endpoint")
	}
}
