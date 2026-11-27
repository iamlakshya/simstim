package egress

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBoundFreshBundleUsesSelectedMemberPrefixWithoutChangingPrimaryAgent(t *testing.T) {
	const name, prefix = "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-"
	args := boundRegistryArgs(map[string]string{name: prefix + strings.Repeat("A", 20)})
	args.Initial.AgentKinds = []string{"claude"}
	args.SourceKinds[name] = []string{"claude"}
	server, result := newBoundRegistryServer(t, args)
	if server.currentMembers().metadata.Agent != "shell" || result.Secrets[name].Shape.Prefix != prefix || !strings.HasPrefix(string(result.Secrets[name].Sentinel), prefix) {
		t.Fatal("fresh bundle used the root agent or fake primary instead of its exact selected member source")
	}
	if server.currentEngine().Decide(Request{Host: "example.com", Port: 443}).Allow {
		t.Fatal("format provenance donated a destination grant")
	}
	args.SourceKinds[name] = []string{"shell"}
	_, rootOnly := newBoundRegistryServer(t, args)
	if rootOnly.Secrets[name].Shape.Prefix != "" {
		t.Fatal("seeded agent union inferred an unselected per-name prefix")
	}
}

func TestBoundFreshSourceRequirementsChooseCompatibleLongestAndRefuseConflict(t *testing.T) {
	previous := agents.Table
	defer func() { agents.Table = previous }()
	row := func(kind, prefix string) agents.Agent {
		return agents.Agent{Kind: kind, Secrets: []agents.SecretBinding{{Env: "CREDENTIAL", Hosts: []string{"example.com"}, Header: "Authorization", Format: "Bearer {value}", Prefix: prefix, Source: "https://example.com/source"}}}
	}
	agents.Table = append(append([]agents.Agent(nil), previous...), row("prefix-short", "P"), row("prefix-long", "PK"))
	args := boundRegistryArgs(map[string]string{"CREDENTIAL": "PKABCD0000"})
	args.Initial.AgentKinds = []string{"prefix-short", "prefix-long"}
	args.SourceKinds["CREDENTIAL"] = []string{"prefix-short", "prefix-long"}
	server, result := newBoundRegistryServer(t, args)
	if result.Secrets["CREDENTIAL"].Shape.Prefix != "PK" || !strings.HasPrefix(string(result.Secrets["CREDENTIAL"].Sentinel), "PK") {
		t.Fatal("compatible selected requirements did not retain every required prefix")
	}
	args.SourceKinds["CREDENTIAL"][0] = "forged"
	if server.currentMembers().sourceKinds["CREDENTIAL"][0] != "prefix-long" {
		t.Fatal("caller mutation changed the owned startup source snapshot")
	}
	cloned, err := cloneMemberState(server.currentMembers())
	if err != nil {
		t.Fatal(err)
	}
	cloned.sourceKinds["CREDENTIAL"][0] = "forged"
	if server.currentMembers().sourceKinds["CREDENTIAL"][0] != "prefix-long" {
		t.Fatal("prospective mutation changed a prior immutable source snapshot")
	}
	args.SourceKinds["CREDENTIAL"] = []string{"prefix-short", "prefix-long"}
	agents.Table[len(agents.Table)-1].Secrets[0].Prefix = "ZZ"
	if _, _, err := validateStartupSources(t.Context(), args); err == nil {
		t.Fatal("conflicting selected prefixes reached token generation")
	}
}

func TestBoundStartupSourceMapRejectsNameKindDomainAndPresenceForgery(t *testing.T) {
	for _, mutate := range []func(*BoundInitArgs){
		func(a *BoundInitArgs) { delete(a.SourceKinds, "KEY") },
		func(a *BoundInitArgs) { a.SourceKinds["OTHER"] = a.SourceKinds["KEY"]; delete(a.SourceKinds, "KEY") },
		func(a *BoundInitArgs) { a.SourceKinds["KEY"] = []string{"claude"} },
		func(a *BoundInitArgs) { a.SourceKinds["KEY"] = []string{"unknown/raw"} },
		func(a *BoundInitArgs) { a.SourceKinds["KEY"] = []string{"shell", "shell"} },
		func(a *BoundInitArgs) { a.SourceKinds["KEY"] = []string{} },
		func(a *BoundInitArgs) { a.Initial.Secrets = nil },
		func(a *BoundInitArgs) { a.Modes["KEY"] = api.SecretModeExposed; a.Binds = nil },
	} {
		args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
		mutate(&args)
		if _, _, err := validateStartupSources(t.Context(), args); err == nil {
			t.Fatal("forged source name/kind/domain was accepted before initialisation")
		}
	}
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	args.SourceKinds["KEY"] = []string{""}
	if _, _, err := validateStartupSources(t.Context(), args); err != nil {
		t.Fatal("explicit custom/no-builtin source was replaced by an unknown raw agent label")
	}
}

func TestBoundInitialFrameCountsTheCompleteSelectedSourceField(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(proto.Request{ID: math.MaxUint64, Op: CtlBoundInit, Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	args.Initial.SessionRef = strings.Repeat("p", proto.MaxFrame-1-len(request))
	if err := checkBoundInitialFrame(args); err != nil {
		t.Fatal("exactly representable initial request hit a smaller cap")
	}
	args.SourceKinds["KEY"] = append(args.SourceKinds["KEY"], "")
	if _, _, err := validateStartupSources(t.Context(), args); err != nil {
		t.Fatal("frame boundary fixture did not retain valid distinct selected sources")
	}
	requireBoundError(t, checkBoundInitialFrame(args), "state_too_large")
}
