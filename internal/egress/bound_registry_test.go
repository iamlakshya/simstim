package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func boundRegistryArgs(values map[string]string) BoundInitArgs {
	args := BoundInitArgs{Version: MemberRegistryV2, Initial: InitArgs{SessionID: "bound234", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: values}, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind), SourceKinds: make(map[string][]string)}
	for name := range values {
		args.Modes[name] = api.SecretModeBound
		args.Binds[name] = api.SecretBind{Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}"}
		args.SourceKinds[name] = []string{"shell"}
	}
	return args
}

func newBoundRegistryServer(t *testing.T, args BoundInitArgs) (*Server, BoundInitResult) {
	t.Helper()
	server := newBoundControlFixture(t, true).server
	result, err := server.initialiseBoundControl(memberJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	return server, result
}

func requireBoundError(t *testing.T, err error, code string, private ...string) {
	t.Helper()
	var public *proto.Error
	if !errors.As(err, &public) || public.Hint == "" || code != "" && public.Code != code {
		t.Fatalf("registry error = %v, want %q with a hint", err, code)
	}
	for _, marker := range private {
		if strings.Contains(public.Error()+public.Hint, marker) {
			t.Fatal("registry error echoed a real value or private metadata")
		}
	}
}

func TestBoundInitialStateReturnsOnlySafeMetadataAndAddsNoNetworkGrant(t *testing.T) {
	const real = "ABCD0000"
	server, result := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": real}))
	public := result.Secrets["KEY"]
	if result.Version != 2 || result.Generation == "" || public.Mode != api.SecretModeBound || !public.Armed || public.Identity == "" || public.Shape == nil || public.Sentinel == real || len(public.Sentinel) != len(real) {
		t.Fatal("bound initial result lost its safe shape or returned the actual value")
	}
	raw, err := json.Marshal(result)
	if err != nil || strings.Contains(string(raw), real) || strings.Contains(string(raw), "private_key") || strings.Contains(string(raw), "history") {
		t.Fatal("public initial result contained actual values, history or signing material")
	}
	if server.currentMembers().metadata.Secrets != nil || server.currentEngine().Decide(Request{Host: "example.com", Port: 443}).Allow {
		t.Fatal("binding persisted a value in metadata or donated a network grant")
	}
	if got := server.scrubber.ScrubText(real + " " + string(public.Sentinel)); got != "[KEY] [KEY]" {
		t.Fatal("initial value and its stand-in did not share canonical redaction")
	}
	public.Shape.Slots[0].Class = "caller-mutation"
	if server.currentMembers().secrets["KEY"].bound.public.Slots[0].Class == "caller-mutation" {
		t.Fatal("public result aliased immutable registry metadata")
	}
}

func TestBoundAliasesRequireFullFreshCohortAndKeepIdempotentHistory(t *testing.T) {
	const beforeValue, afterValue = "ABCD0000", "WXYZ1111"
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": beforeValue, "B": beforeValue}))
	if initial.Secrets["A"].Sentinel != initial.Secrets["B"].Sentinel || initial.Secrets["A"].Identity != initial.Secrets["B"].Identity {
		t.Fatal("deliberate equal aliases acquired ambiguous stand-ins")
	}
	before, engine, redaction := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	_, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"A": afterValue}}))
	requireBoundError(t, err, "secret_alias", beforeValue, afterValue)
	if before != server.currentMembers() || engine != server.currentEngine() || redaction != server.scrubber.snapshot() {
		t.Fatal("partial alias approval changed registry or engine state")
	}
	result, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"A": afterValue, "B": afterValue}}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Generation == initial.Generation || result.Secrets["A"].Sentinel != initial.Secrets["A"].Sentinel || result.Secrets["B"].Identity != initial.Secrets["B"].Identity {
		t.Fatal("compatible group rotation changed the guest environment identity")
	}
	for _, name := range []string{"A", "B"} {
		entry := server.currentMembers().secrets[name]
		if entry.value != afterValue || !reflect.DeepEqual(entry.history, []api.Secret{beforeValue}) {
			t.Fatal("group rotation lost a same-name prior value")
		}
	}
	if got := server.scrubber.ScrubText(beforeValue + " " + afterValue); got != "[A] [A]" || strings.Contains(got, "#") {
		t.Fatal("rotation fabricated another audit name or lost old redaction")
	}
	before, engine, redaction = server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	_, err = server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: result.Generation, Values: map[string]api.Secret{"A": afterValue}}))
	if err != nil || before != server.currentMembers() || engine != server.currentEngine() || redaction != server.scrubber.snapshot() {
		t.Fatal("same-value alias subset rearmed state or grew history")
	}
}

func TestBoundDistinctBindingsUseActualResponseIdentityForTheSameValue(t *testing.T) {
	const real = "ABCD0000"
	args := boundRegistryArgs(map[string]string{"A": real, "B": real})
	args.Binds["B"] = api.SecretBind{Hosts: []string{"example.com"}, Header: "X-Api-Key", Format: "{value}"}
	server, initial := newBoundRegistryServer(t, args)
	if initial.Secrets["A"].Sentinel == initial.Secrets["B"].Sentinel || initial.Secrets["A"].Identity == initial.Secrets["B"].Identity {
		t.Fatal("different binding identities were merged as a random collision")
	}
	for _, name := range []string{"A", "B"} {
		view, err := server.responseSecretSnapshot(t.Context(), "example.com:443", boundPrimaryUse(server, name))
		if err != nil {
			t.Fatal(err)
		}
		if got, valid := view.exact(real); !valid || got != initial.Secrets[name].Sentinel {
			t.Fatal("response selected an identity that the request did not use")
		}
	}
	view, err := server.responseSecretSnapshot(t.Context(), "example.com:443", secretRequestUse{})
	if err != nil || view.match([]byte(real), true).err == nil {
		t.Fatal("unresolved actual candidate selected an arbitrary credential identity")
	}
}

func TestBoundRestoreCreatesFreshUnarmedAliasSentinels(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"A": "ABCD0000", "B": "ABCD0000"})
	_, previous := newBoundRegistryServer(t, args)
	args.Initial.Secrets = nil
	args.SourceKinds = nil
	args.Saved = api.ClonePublicSecretStates(previous.Secrets)
	server, restored := newBoundRegistryServer(t, args)
	if restored.Secrets["A"].Armed || restored.Secrets["B"].Armed || restored.Secrets["A"].Sentinel == previous.Secrets["A"].Sentinel || restored.Secrets["A"].Sentinel != restored.Secrets["B"].Sentinel {
		t.Fatal("restart retained a real value, replayed a stand-in or split saved aliases")
	}
	if server.currentMembers().secrets["A"].present || server.currentMembers().secrets["A"].value != "" {
		t.Fatal("unarmed restart retained an actual credential")
	}
	_, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: restored.Generation, Values: map[string]api.Secret{"A": previous.Secrets["A"].Sentinel, "B": previous.Secrets["A"].Sentinel}}))
	requireBoundError(t, err, "secret_collision")
}

func TestBoundControlRefusesFutureFieldsDuplicateAliasesAndInvalidShapesWithoutPublication(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	for _, raw := range [][]byte{
		memberJSON(t, ArmArgs{Version: 1, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}),
		memberJSON(t, ArmArgs{Version: 2, Generation: "stale", Values: map[string]api.Secret{"KEY": "WXYZ1111"}}),
		memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"UNKNOWN": "WXYZ1111"}}),
		memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "different-shape"}}),
		[]byte(`{"version":2,"version":1,"values":{}}`),
		[]byte(`{"version":2,"values":{},"binds":{"KEY":{"hosts":["other.example.com"]}}}`),
	} {
		if _, err := server.armControl(raw); err == nil {
			t.Fatal("unsupported or mismatched arm was accepted")
		}
		if server.currentMembers() != before || server.currentEngine() != engine || server.scrubber.snapshot() != scrub {
			t.Fatal("known-negative arm altered live canonical state")
		}
	}
}

func TestBoundSecretBearingValuesAndMetadataRedactEveryFormatter(t *testing.T) {
	const marker = "private-marker-never-print"
	objects := []any{
		BoundInitArgs{Initial: InitArgs{Secrets: map[string]string{"KEY": marker}}},
		MemberAddV2Args{Values: map[string]api.Secret{"KEY": marker}},
		MemberAddResultV2{Env: map[string]api.Secret{"KEY": marker}},
		ArmArgs{Values: map[string]api.Secret{"KEY": marker}},
		api.PublicSecretState{Sentinel: marker}, api.SecretShape{Prefix: marker},
		api.SecretShapeSlot{ASCII: marker}, registeredMint{value: marker}, boundSecret{sentinel: marker},
		boundMemberCandidate{env: map[string]api.Secret{"KEY": marker}},
	}
	for _, object := range objects {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if text := fmt.Sprintf(verb, object); text != api.Redacted || strings.Contains(text, marker) {
				t.Fatal("a derived registry value bypassed explicit redacting formatting")
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepareBoundInitial(ctx, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled bound preparation continued to mutation admission")
	}
}
