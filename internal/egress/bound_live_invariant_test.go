package egress

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundMemberAliasSortedBeforeExistingNameKeepsEveryLiveStandIn(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"B": "ABCD0000", "C": "ABCD0000"}))
	args := MemberAddV2Args{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"A": "WXYZ1111", "B": "WXYZ1111", "C": "WXYZ1111"}, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	for name := range args.Values {
		args.Modes[name], args.Binds[name] = api.SecretModeBound, boundRegistryArgs(map[string]string{name: ""}).Binds[name]
	}
	result, err := server.addBoundControlMember(memberJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"B", "C"} {
		if result.Env[name] != initial.Secrets[name].Sentinel || result.Secrets[name].Identity != initial.Secrets[name].Identity {
			t.Fatal("joining member replaced an existing live environment or Store stand-in")
		}
	}
	if result.Env["A"] != initial.Secrets["B"].Sentinel || result.Secrets["A"].Identity != initial.Secrets["B"].Identity {
		t.Fatal("a freshly approved exact alias did not reuse the unchanged boot identity")
	}
}

func TestBoundImpossibleLiveRotationRefusesBeforeClosingAnyResource(t *testing.T) {
	for _, viaMember := range []bool{false, true} {
		for _, replacement := range []string{"stand-in", "different-shape"} {
			server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
			scope := boundRequestScope(t, server, []string{"KEY"})
			before, engine, redaction := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
			value := api.Secret("shape-does-not-match")
			if replacement == "stand-in" {
				value = initial.Secrets["KEY"].Sentinel
			}
			var err error
			if viaMember {
				args := MemberAddV2Args{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": value}, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: boundRegistryArgs(map[string]string{"KEY": ""}).Binds}
				_, err = server.addBoundControlMember(memberJSON(t, args))
			} else {
				_, err = server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": value}}))
			}
			if err == nil || scope.ctx.Err() != nil || before != server.currentMembers() || engine != server.currentEngine() || redaction != server.scrubber.snapshot() {
				t.Fatal("impossible live rebinding disturbed an existing resource or state before refusing")
			}
		}
	}
}

func TestBoundStrictV2RefusesCaseAliasesAtEveryPublicMetadataLevel(t *testing.T) {
	for _, raw := range []string{
		`{"Version":2,"generation":"g","values":{}}`,
		`{"version":2,"generation":"g","Values":{}}`,
		`{"version":2,"initial":{},"modes":{},"saved":{"KEY":{"mode":"bound","Shape":{"version":1,"slots":[]}}}}`,
		`{"version":2,"initial":{},"modes":{},"binds":{"KEY":{"Hosts":["example.com"],"header":"Authorization","format":"Bearer {value}"}}}`,
		`{"version":2,"initial":{},"modes":{},"saved":{"KEY":{"mode":"bound","shape":{"version":1,"slots":[{"Class":"ascii-upper","count":1,"width":1}]}}}}`,
	} {
		var args any
		if strings.Contains(raw, "initial") {
			args = new(BoundInitArgs)
		} else {
			args = new(ArmArgs)
		}
		if err := decodeBoundControlArgs([]byte(raw), args); err == nil {
			t.Fatal("v2 accepted an unknown case alias instead of an exact wire tag")
		}
	}
}

func TestBoundStrictV2AcceptsCanonicalPromotedPolicyFieldsAndTimeScalars(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	methods := []string{"GET"}
	args.Initial.Policy.LayerValues.Network.Allow = []string{"example.com:443"}
	args.Initial.Policy.Sources = []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Network: api.NetworkValues{Deny: []string{"other.example.com:443"}}}}}
	args.Initial.Policy.Rules = []api.Rule{{Allow: true, Door: api.DoorNet, Target: "example.com:443", Methods: &methods, Until: time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)}}
	var decoded BoundInitArgs
	if err := decodeBoundControlArgs(memberJSON(t, args), &decoded); err != nil {
		t.Fatal("exact tag validation rejected valid promoted policy fields or their scalar decoder")
	}
	if len(decoded.Initial.Policy.Network.Allow) != 1 || len(decoded.Initial.Policy.Sources) != 1 || len(decoded.Initial.Policy.Sources[0].Network.Deny) != 1 || !decoded.Initial.Policy.Rules[0].Until.Equal(args.Initial.Policy.Rules[0].Until) {
		t.Fatal("strict field validation changed the canonical policy decoder's result")
	}
}
