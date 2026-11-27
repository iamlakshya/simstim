package egress

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestLivePolicyControlRecomputesFromAppliedStateAndAcknowledgesCanonicalSources(t *testing.T) {
	sources := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: new("process")}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}}
	applied, canonical, err := MergeLayers(sources)
	if err != nil {
		t.Fatal(err)
	}
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: applied})
	ledger := server.ledger
	proposal := api.CloneLayers(canonical)
	proposal[0].Sandbox.Wall = new("microvm")
	proposal[0].Network.Deny = []string{"blocked.example"}
	_, proposal, err = MergeLayers(proposal)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := api.HashPolicyLaptopLayersV1(proposal)
	if err != nil {
		t.Fatal(err)
	}
	// This conflicting echo cannot choose the applied network or native wall.
	echo := api.Policy{Preset: api.PresetOpen, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: new("none")}}}
	var wire bytes.Buffer
	server.handleCtlRequest(proto.NewWriter(&wire), proto.Request{ID: 1, Op: CtlPolicyUpdate, Args: memberJSON(t, PolicyUpdateArgs{Policy: echo, SourceLayers: proposal})})
	frame, err := proto.Read(bufio.NewReader(&wire))
	if err != nil {
		t.Fatal(err)
	}
	var response proto.Response
	var result struct {
		Hash string `json:"hash"`
	}
	if json.Unmarshal(frame.Payload, &response) != nil || !response.OK || !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.Hash != hash {
		t.Fatal("actual update did not acknowledge its canonical carried sources")
	}
	current := server.currentMembers().metadata.Policy
	if current.Sandbox.Wall == nil || *current.Sandbox.Wall != "process" || server.currentEngine().Decide(Request{Host: "blocked.example", Port: 443}).Allow || server.ledger != ledger {
		t.Fatal("echo changed applied wall, lost the live deny or replaced the ledger")
	}
}

func TestLivePolicyControlRejectsMalformedSourcesWithoutPublishingEcho(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}})
	state, engine := server.currentMembers(), server.currentEngine()
	hash, err := server.replaceControlPolicyWithHash([]byte(`{"policy":{"preset":"open"},"source_layers":[]}`))
	if err == nil || hash != "" || server.currentMembers() != state || server.currentEngine() != engine {
		t.Fatal("invalid carried source vector published the policy echo")
	}
	hash, err = server.replaceControlPolicyWithHash(memberJSON(t, PolicyUpdateArgs{Policy: state.metadata.Policy}))
	if err != nil || hash != "" {
		t.Fatal("ordinary policy update gained a source acknowledgement", err)
	}
}
