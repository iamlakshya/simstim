package egress

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func memberIntent(generation string, values map[string]api.Secret, kind string) MemberAddV2Args {
	intent := MemberAddV2Args{Version: 2, Generation: generation, Kind: kind, Values: values, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)}
	for name := range values {
		intent.Modes[name] = api.SecretModeBound
		intent.Binds[name] = api.SecretBind{Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}"}
	}
	return intent
}

func TestBoundMemberUsesTheSameFullAliasTransactionAndReturnsExactPersistenceMetadata(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": "ABCD0000", "B": "ABCD0000"}))
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	_, err := server.addBoundControlMember(memberJSON(t, memberIntent(initial.Generation, map[string]api.Secret{"A": "WXYZ1111"}, "codex")))
	requireBoundError(t, err, "secret_alias")
	if before != server.currentMembers() || engine != server.currentEngine() || scrub != server.scrubber.snapshot() {
		t.Fatal("failed member rotation published another builtin or credential")
	}
	result, err := server.addBoundControlMember(memberJSON(t, memberIntent(initial.Generation, map[string]api.Secret{"A": "WXYZ1111", "B": "WXYZ1111", "C": "WXYZ1111"}, "codex")))
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 2 || result.Generation == initial.Generation || !reflect.DeepEqual(slices.Sorted(maps.Keys(result.Env)), []string{"A", "B", "C"}) || !reflect.DeepEqual(slices.Sorted(maps.Keys(result.Secrets)), []string{"A", "B", "C"}) {
		t.Fatal("member transaction returned extra names or lost exact public persistence metadata")
	}
	for _, name := range []string{"A", "B", "C"} {
		if result.Env[name] != initial.Secrets["A"].Sentinel || result.Secrets[name].Sentinel != result.Env[name] || result.Secrets[name].Mode != api.SecretModeBound || result.Secrets[name].Shape == nil {
			t.Fatal("fresh alias member changed an existing environment or returned a real value")
		}
	}
	before, engine, scrub = server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	if _, err := server.addBoundControlMember(memberJSON(t, memberIntent(result.Generation, map[string]api.Secret{"C": "WXYZ1111"}, "codex"))); err != nil || before != server.currentMembers() || engine != server.currentEngine() || scrub != server.scrubber.snapshot() {
		t.Fatal("same member retry grew history or published another generation")
	}
}

func TestBoundMemberRefusesModeBindingAndSentinelCollisionsWithoutChangingState(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": "ABCD0000"}))
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	mode := memberIntent(initial.Generation, map[string]api.Secret{"A": "ABCD0000"}, "")
	mode.Modes["A"], mode.Binds = api.SecretModeExposed, nil
	binding := memberIntent(initial.Generation, map[string]api.Secret{"A": "ABCD0000"}, "")
	binding.Binds["A"] = api.SecretBind{Hosts: []string{"other.example.com"}, Header: "Authorization", Format: "Bearer {value}"}
	for _, intent := range []MemberAddV2Args{mode, binding, memberIntent(initial.Generation, map[string]api.Secret{"B": initial.Secrets["A"].Sentinel}, "claude")} {
		if _, err := server.addBoundControlMember(memberJSON(t, intent)); err == nil {
			t.Fatal("mode, binding or known public-stand-in collision was accepted")
		}
		if before != server.currentMembers() || engine != server.currentEngine() || scrub != server.scrubber.snapshot() {
			t.Fatal("known-negative member operation changed a current identity")
		}
	}
}

func TestBoundPolicyAndMemberUpdatesKeepTheExistingOnceLedger(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	args.Initial.Policy = onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "api.example.com:8443", Once: true, GrantID: "kept-bound-instance"})
	server, initial := newBoundRegistryServer(t, args)
	request := Request{Host: "api.example.com", Port: 8443}
	permit := server.currentEngine().NewPermit()
	if !server.currentEngine().DecideWithPermit(request, permit).Allow {
		t.Fatal("once fixture was refused")
	}
	permit.Commit()
	if _, err := server.addBoundControlMember(memberJSON(t, memberIntent(initial.Generation, nil, "codex"))); err != nil {
		t.Fatal(err)
	}
	if !server.currentEngine().Revalidate(request, permit).Allow || server.currentEngine().Decide(request).Allow {
		t.Fatal("v2 membership replaced or rearmed the canonical once ledger")
	}
	permit.Close()
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: args.Initial.Policy})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("v2 policy replacement forgot a spent once instance")
	}
}

func TestBoundWholeCurrentStateLimitIncludesPriorValuesAndSafeMetadata(t *testing.T) {
	const size = 1 << 20
	shape := sentinelShape{Slots: []sentinelSlot{{Alphabet: sentinelUpper, Count: size}}}
	public, err := publicSecretShape(t.Context(), "KEY", shape, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := canonicalRegistryBinding("KEY", api.SecretBind{Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}"})
	if err != nil {
		t.Fatal(err)
	}
	entry := registeredSecret{mode: api.SecretModeBound, present: true, value: api.Secret(strings.Repeat("A", size)), identity: "size-fixture", history: []api.Secret{api.Secret(strings.Repeat("C", size))}, bound: &boundSecret{binding: binding, shape: shape, public: public, sentinel: api.Secret(strings.Repeat("B", size))}}
	state := &memberState{version: 2, generation: "size-generation", metadata: InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}}, kinds: []string{"shell"}, secrets: map[string]registeredSecret{"KEY": entry}}
	if err := checkBoundStateSize(t.Context(), state); err != nil {
		t.Fatal("a representable full state hit a smaller value/count limit")
	}
	entry.history = append(entry.history, api.Secret(strings.Repeat("D", size)))
	state.secrets["KEY"] = entry
	requireBoundError(t, checkBoundStateSize(t.Context(), state), "state_too_large")
}

func TestBoundMintCommitsSameRegistryBeforeReturningAndIsIdempotent(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	before := server.currentMembers()
	sentinel, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
	if err != nil || sentinel == value || len(sentinel) != len(value) || before == server.currentMembers() {
		t.Fatalf("mint transaction did not publish a valid stand-in: %v", err)
	}
	view, err := server.responseSecretSnapshot(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if actual, committed := view.exact(value); !committed || actual != sentinel || server.scrubber.ScrubText(string(value)) != "[KEY]" {
		t.Fatal("mint returned before same-name redaction and selected echo publication")
	}
	before = server.currentMembers()
	again, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
	if err != nil || again != sentinel || before != server.currentMembers() || len(before.secrets["KEY"].minted) != 1 {
		t.Fatal("same minted value created a second identity or unbounded history")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := server.registerResponseSecret(ctx, "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key"); err == nil || before != server.currentMembers() {
		t.Fatal("cancelled request callback published registry state")
	}
}
