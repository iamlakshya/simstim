package egress

import (
	"bufio"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestMemberCapabilityDiscoveryBeforeInitHasNoAuthorityOrTrustPayload(t *testing.T) {
	server := uninitialisedMemberServer(t)
	engine, scrub := server.currentEngine(), server.scrubber.snapshot()
	var wire bytes.Buffer
	server.handleCtlRequest(proto.NewWriter(&wire), proto.Request{ID: 1, Op: CtlCapabilities})
	frame, err := proto.Read(bufio.NewReader(&wire))
	if err != nil {
		t.Fatal(err)
	}
	var response proto.Response
	var caps CapabilitiesResult
	if json.Unmarshal(frame.Payload, &response) != nil || !response.OK || json.Unmarshal(response.Result, &caps) != nil || !reflect.DeepEqual(caps.Features, []string{MemberAddV1Feature, MemberAddV2Feature, api.FeatureBoundSecrets}) {
		t.Fatal("member capability discovery failed before init")
	}
	if bytes.Contains(frame.Payload, []byte("CERTIFICATE")) || server.isInited() || server.currentMembers() != nil || server.currentEngine() != engine || server.scrubber.snapshot() != scrub {
		t.Fatal("capability discovery changed authority or disclosed trust data")
	}
	if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex"})); err == nil {
		t.Fatal("member addition succeeded before initial admission")
	}
}

func TestMemberCapabilitiesStaySeparateFromPublicCAAndEventsHoldOnlyNames(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}})
	var wire bytes.Buffer
	writer := proto.NewWriter(&wire)
	server.ctl = writer
	server.handleCtlRequest(writer, proto.Request{ID: 1, Op: CtlCapabilities})
	server.handleCtlRequest(writer, proto.Request{ID: 2, Op: CtlMemberAdd, Args: memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"KEY": "sensitive-control-value"}})})
	reader := bufio.NewReader(&wire)
	frame, err := proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	var response proto.Response
	if json.Unmarshal(frame.Payload, &response) != nil || !response.OK {
		t.Fatal("capabilities failed")
	}
	var caps CapabilitiesResult
	if json.Unmarshal(response.Result, &caps) != nil || !reflect.DeepEqual(caps.Features, []string{MemberAddV1Feature, MemberAddV2Feature, api.FeatureBoundSecrets}) {
		t.Fatal("member capability was not advertised")
	}
	frame, err = proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	var event CtlEvent
	if frame.Type != proto.TEvent || json.Unmarshal(frame.Payload, &event) != nil || event.Event != EventMemberAdd || event.Member == nil || !reflect.DeepEqual(event.Member.Secrets, []string{"KEY"}) || event.Member.Kind != "codex" {
		t.Fatal("member event did not contain exactly the trusted kind and requested names")
	}
	if bytes.Contains(frame.Payload, []byte("sensitive-control-value")) {
		t.Fatal("member audit included a secret value")
	}
	frame, err = proto.Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	response = proto.Response{}
	if json.Unmarshal(frame.Payload, &response) != nil || !response.OK || response.ID != 2 {
		t.Fatal("member result missing after its names-only event")
	}
	var result MemberAddResult
	if json.Unmarshal(response.Result, &result) != nil || result.Version != 1 || len(result.Env) != 1 || result.Env["KEY"] != "sensitive-control-value" {
		t.Fatal("requested legacy exposure result was not preserved")
	}
	ca, err := json.Marshal(InitResult{CACert: string(server.ca.publicPEM)})
	if err != nil || bytes.Contains(ca, []byte("PRIVATE KEY")) || bytes.Contains(ca, []byte("sensitive-control-value")) {
		t.Fatal("initial public trust result included secret material")
	}
}

func TestConcurrentMemberAndPolicyTransactionsKeepTheWholeState(t *testing.T) {
	policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny}
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: policy, Secrets: map[string]string{"OLD": "old-sensitive-value"}})
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	for _, member := range []MemberAddArgs{
		{Version: 1, Kind: "claude", ExposedValues: map[string]api.Secret{"FIRST": "first-sensitive-value"}},
		{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"SECOND": "second-sensitive-value"}},
	} {
		raw := memberJSON(t, member)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := server.addControlMember(raw); errors <- err }()
	}
	rawPolicy := memberJSON(t, PolicyUpdateArgs{Policy: policy})
	wg.Add(1)
	go func() { defer wg.Done(); errors <- server.replaceControlPolicy(rawPolicy) }()
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	state := server.currentMembers()
	if len(state.secrets) != 3 || !reflect.DeepEqual(state.kinds, []string{"claude", "codex", "shell"}) || !reflect.DeepEqual(state.metadata.Policy, policy) {
		t.Fatal("concurrent control operations lost registry, kinds or policy")
	}
	for _, value := range []string{"old-sensitive-value", "first-sensitive-value", "second-sensitive-value"} {
		if strings.Contains(server.scrubber.ScrubText(value), value) {
			t.Fatal("concurrent publication lost redaction")
		}
	}
}

func TestFailedProspectiveBuiltinDoesNotPublishValuesOrKind(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"OLD": "old-sensitive-value"}})
	state, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	previous := agents.Table
	rows := append([]agents.Agent(nil), previous...)
	agents.Table = append(rows, agents.Agent{Kind: "broken-builtin", Egress: []string{"invalid sensitive-construction-value"}})
	defer func() { agents.Table = previous }()
	_, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "broken-builtin", ExposedValues: map[string]api.Secret{"NEW": "sensitive-construction-value"}}))
	if err == nil || strings.Contains(err.Error(), "sensitive-construction-value") {
		t.Fatal("prospective engine failure was accepted or disclosed a value")
	}
	if server.currentMembers() != state || server.currentEngine() != engine || server.scrubber.snapshot() != scrub {
		t.Fatal("failed engine construction partially published state")
	}
}

func TestLegacyUnknownInitialKindKeepsNoBuiltinAndFutureRegistryCannotExpose(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "legacy-unknown", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"KEY": "kept-sensitive-value"}})
	if server.currentEngine().Decide(Request{Host: "api.openai.com", Port: 443}).Allow {
		t.Fatal("legacy unknown initial kind gained authority")
	}
	state := server.currentMembers()
	// This represents a future in-memory version, not a guessed v1 binding.
	// An old adapter must refuse it rather than projecting its real values.
	state.version = 2
	if _, err := state.initialArgs(); err == nil {
		t.Fatal("future registry silently became legacy exposure")
	}
	state.version = 1
	entry := state.secrets["KEY"]
	entry.identity = "future-bound-v2"
	state.secrets["KEY"] = entry
	if _, err := state.initialArgs(); err == nil {
		t.Fatal("future binding silently became legacy exposure")
	}
}
