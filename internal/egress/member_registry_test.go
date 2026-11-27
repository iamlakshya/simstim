package egress

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func memberJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode test control state")
	}
	return raw
}

func uninitialisedMemberServer(t *testing.T) *Server {
	t.Helper()
	conn, peer := net.Pipe()
	server, err := NewServer(conn, Options{Dialer: &Dialer{}, OriginRoots: x509.NewCertPool()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close(); _ = server.Close() })
	return server
}

func newMemberServer(t *testing.T, args InitArgs) *Server {
	t.Helper()
	server := uninitialisedMemberServer(t)
	if _, err := server.initialiseControl(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestMemberRegistryAddsOnlyRequestedEnvironmentAndKeepsEveryRedaction(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"OLD_KEY": "old-sensitive-value"}})
	result, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"NEW_KEY": "new-sensitive-value"}}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || len(result.Env) != 1 || result.Env["NEW_KEY"] != "new-sensitive-value" {
		t.Fatal("member reply did not contain precisely its requested environment")
	}
	if _, exposedOld := result.Env["OLD_KEY"]; exposedOld {
		t.Fatal("member reply exposed an unrequested old value")
	}
	if got := server.scrubber.ScrubText("old-sensitive-value new-sensitive-value"); got != "[OLD_KEY] [NEW_KEY]" {
		t.Fatal("member addition discarded old or new secret redaction")
	}
	result.Env["NEW_KEY"] = "caller-mutation"
	if got := server.scrubber.ScrubText("new-sensitive-value"); got != "[NEW_KEY]" {
		t.Fatal("reply map aliased held registry state")
	}
	state, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	_, err = server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"NEW_KEY": "new-sensitive-value"}}))
	if err != nil {
		t.Fatal(err)
	}
	if server.currentMembers() != state || server.currentEngine() != engine || server.scrubber.snapshot() != scrub {
		t.Fatal("identical member values and kind were not a publication no-op")
	}
}

func TestMemberRegistryRefusesChangesAndFutureBindingsWithoutPublication(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Secrets: map[string]string{"KEY": "kept-sensitive-value"}, Policy: api.Policy{Preset: api.PresetLocked}})
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	for _, raw := range [][]byte{
		memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"KEY": "changed-sensitive-value", "EXTRA": "extra-sensitive-value"}}),
		[]byte(`{"version":1,"kind":"codex","exposed_values":{"EXTRA":"extra-sensitive-value"},"bindings":{"EXTRA":{"hosts":["example.com"]}}}`),
		[]byte(`{"version":1,"kind":"codex","exposed_values":{"EXTRA":{"value":"extra-sensitive-value","binding":{"mode":"bound"}}}}`),
		[]byte(`{"version":2,"kind":"codex","exposed_values":{"EXTRA":"extra-sensitive-value"}}`),
		[]byte(`{"version":1,"kind":"unknown-kind","exposed_values":{"EXTRA":"extra-sensitive-value"}}`),
		[]byte(`{"version":1,"kind":"codex","guest_kind":"claude","trusted":true}`),
	} {
		if _, err := server.addControlMember(raw); err == nil {
			t.Fatal("changed, unsupported or untrusted member form was accepted")
		} else if strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal("member rejection disclosed a value")
		}
		if server.currentMembers() != before || server.currentEngine() != engine || server.scrubber.snapshot() != scrub {
			t.Fatal("failed member addition partially published its kind, engine or registry")
		}
	}
	if server.currentEngine().Decide(Request{Host: "api.openai.com", Port: 443}).Allow {
		t.Fatal("a rejected member added its builtin authority")
	}
}

func TestMemberBuiltinUnionKeepsUserDeniesLocksAndPolicyUpdates(t *testing.T) {
	policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny,
		Rules: []api.Rule{{Layer: api.LayerSession, Door: api.DoorNet, Target: "api.openai.com", Allow: false}},
		Locks: []api.Lock{{Layer: api.LayerHost, Deny: []string{"api.anthropic.com"}}}}
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: policy})
	for _, kind := range []string{"codex", "claude"} {
		if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: kind})); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"api.openai.com", "api.anthropic.com"} {
		if server.currentEngine().Decide(Request{Host: host, Port: 443}).Allow {
			t.Fatal("builtin union bypassed a user deny or lock")
		}
	}
	if !server.currentEngine().Decide(Request{Host: "auth.openai.com", Port: 443}).Allow {
		t.Fatal("canonical builtin union was not installed")
	}
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	if !server.currentEngine().Decide(Request{Host: "auth.openai.com", Port: 443}).Allow {
		t.Fatal("policy replacement discarded the saved member union")
	}
	if !reflect.DeepEqual(server.currentMembers().metadata.Policy, policy) {
		t.Fatal("member union changed user policy metadata")
	}
}

func TestMemberUnionPreservesOwnedAndSpentOnceInstances(t *testing.T) {
	policy := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "example.com:8443", Once: true, GrantID: "kept-once-instance"})
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: policy})
	request := Request{Host: "example.com", Port: 8443}
	permit := server.currentEngine().NewPermit()
	if !server.currentEngine().DecideWithPermit(request, permit).Allow {
		t.Fatal("initial once reservation failed")
	}
	permit.Commit()
	if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex"})); err != nil {
		t.Fatal(err)
	}
	if !server.currentEngine().Revalidate(request, permit).Allow {
		t.Fatal("member union revoked an owned once grant")
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("member union rearmed a reserved once instance")
	}
	ended := permit.Close()
	if len(ended) != 1 || ended[0].GrantID != "kept-once-instance" {
		t.Fatal("member union changed the exact once identity")
	}
	if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "claude"})); err != nil {
		t.Fatal(err)
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("a later member union rearmed a spent instance")
	}
}

func TestMemberSeedUsesCanonicalSavedKindsAndKeepsLegacyUnknownMeaning(t *testing.T) {
	legacy, err := NewEngine("legacy-unknown", api.Policy{Preset: api.PresetLocked})
	if err != nil || legacy.Decide(Request{Host: "api.openai.com", Port: 443}).Allow {
		t.Fatal("legacy unknown kind gained builtin authority or lost compatibility")
	}
	// The host reconstructs this finite set from saved members, including
	// ended members. It fetches fresh approved values independently of egress.
	args := InitArgs{Agent: "shell", AgentKinds: []string{"codex", "claude", "codex"}, Policy: api.Policy{Preset: api.PresetLocked}}
	first := newMemberServer(t, args)
	second := newMemberServer(t, args)
	for _, server := range []*Server{first, second} {
		if !server.currentEngine().Decide(Request{Host: "api.openai.com", Port: 443}).Allow || !server.currentEngine().Decide(Request{Host: "api.anthropic.com", Port: 443}).Allow {
			t.Fatal("fresh start omitted a trusted saved kind")
		}
		if !reflect.DeepEqual(server.currentMembers().kinds, []string{"claude", "codex", "shell"}) {
			t.Fatal("saved kind union was not canonical and finite")
		}
	}
	if _, err := prepareInitialState(InitArgs{AgentKinds: []string{"legacy-unknown"}}); err == nil {
		t.Fatal("an explicit unknown seed was treated as trusted membership")
	}
}

func TestMemberSecretBearingObjectsNeverFormatValues(t *testing.T) {
	value := api.Secret("distinct-sensitive-value")
	objects := []any{
		InitArgs{Secrets: map[string]string{"KEY": string(value)}},
		MemberAddArgs{Version: 1, Kind: "shell", ExposedValues: map[string]api.Secret{"KEY": value}},
		MemberAddResult{Version: 1, Env: map[string]api.Secret{"KEY": value}},
		registeredSecret{value: value, identity: legacyExposedIdentity},
	}
	for _, object := range objects {
		if strings.Contains(fmt.Sprintf("%v %+v %#v %s %q %x %X", object, object, object, object, object, object, object), string(value)) {
			t.Fatal("a secret-bearing member object exposed its value through formatting")
		}
	}
}

func TestCustomMemberExposureAddsNoBuiltinAuthority(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}})
	result, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "", ExposedValues: map[string]api.Secret{"CUSTOM_KEY": "custom-sensitive-value"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Env) != 1 || result.Env["CUSTOM_KEY"] != "custom-sensitive-value" || !reflect.DeepEqual(server.currentMembers().kinds, []string{"shell"}) {
		t.Fatal("custom command exposure invented a builtin kind or returned the wrong environment")
	}
	for _, host := range []string{"api.openai.com", "api.anthropic.com"} {
		if server.currentEngine().Decide(Request{Host: host, Port: 443}).Allow {
			t.Fatal("custom command gained agent builtin egress")
		}
	}
	if got := server.scrubber.ScrubText("custom-sensitive-value"); got != "[CUSTOM_KEY]" {
		t.Fatal("custom command exposure was not registered for redaction")
	}
}
