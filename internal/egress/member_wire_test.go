package egress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestMemberInitialWireUsesTheExistingExactFrameBoundary(t *testing.T) {
	args := InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"KEY": ""}}
	raw := memberJSON(t, args)
	request, err := json.Marshal(proto.Request{ID: 1, Op: CtlInit, Args: raw})
	if err != nil {
		t.Fatal("cannot encode wire boundary fixture")
	}
	// One type byte belongs to proto.MaxFrame. The length prefix follows
	// proto.Writer's existing convention and is not a smaller new limit.
	valueBytes := proto.MaxFrame - 1 - len(request)
	args.Secrets["KEY"] = strings.Repeat("A", valueBytes)
	prepared, err := prepareInitialState(args)
	if err != nil || prepared == nil {
		t.Fatal("an initial state fitting the existing frame boundary was refused")
	}
	if len(prepared.state.secrets) != 1 || len(prepared.state.metadata.AgentKinds) != 0 {
		t.Fatal("legacy init duplicated its primary kind or lost its registry")
	}
	args.Secrets["KEY"] += "A"
	if _, err := prepareInitialState(args); err == nil {
		t.Fatal("an initial state one byte beyond the existing frame boundary was accepted")
	}
}

func TestMemberWireChecksTheWholeProspectiveStateWithoutSmallValueCaps(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"OLD": strings.Repeat("A", 2<<20)}})
	if _, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"NEW": api.Secret(strings.Repeat("B", 1<<20))}})); err != nil {
		t.Fatal("a representable current registry was rejected by a smaller value/count cap")
	}
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	_, err := server.addControlMember(memberJSON(t, MemberAddArgs{Version: 1, Kind: "claude", ExposedValues: map[string]api.Secret{"EXCESS": api.Secret(strings.Repeat("C", 2<<20))}}))
	if err == nil {
		t.Fatal("individually representable addition overflowed the complete registry")
	}
	if before != server.currentMembers() || engine != server.currentEngine() || scrub != server.scrubber.snapshot() {
		t.Fatal("whole-state wire failure partially committed values or authority")
	}
}

func TestMemberPolicyReplacementUsesTheSameInitialValidator(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}, Secrets: map[string]string{"KEY": "kept-sensitive-value"}})
	before, engine, scrub := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	policy := api.Policy{Preset: api.Preset("kept-sensitive-value")}
	err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy}))
	if err == nil || strings.Contains(err.Error(), "kept-sensitive-value") {
		t.Fatal("invalid prospective policy succeeded or disclosed a value")
	}
	if before != server.currentMembers() || engine != server.currentEngine() || scrub != server.scrubber.snapshot() {
		t.Fatal("failed policy replacement altered the member registry")
	}
}
