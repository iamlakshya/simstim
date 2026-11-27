package supervisor

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestNamedSSHPolicyEditDoesNotChangeTheRunningKeyOwner(t *testing.T) {
	value, key := namedSSHFixture(t)
	agent, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.close)
	layers := []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"DEPLOY_KEY"}}}}}
	s := &Supervisor{id: "policy234", lifetime: t.Context(), rec: record{phase: host.LifeRunning}, firstSaved: true, log: slog.New(slog.DiscardHandler), sshAgent: agent,
		m: &host.Manifest{Schema: 1, ID: "policy234", Wall: &host.WallRecord{Driver: "process", Level: "process"}, Layers: layers, Policy: &api.Policy{LayerValues: layers[0].LayerValues}}}
	oldSave := saveManifest
	var saved host.Manifest
	saveManifest = func(snapshot *host.Manifest) error { saved = host.CloneManifest(*snapshot); return nil }
	t.Cleanup(func() { saveManifest = oldSave })
	client, _ := namedSSHClient(t, agent)
	for _, args := range []api.NamedSSHKeyPolicyArgs{{V: 1, Session: s.id, Name: "OTHER_KEY", Allow: true}, {V: 1, Session: s.id, Name: "DEPLOY_KEY", Allow: false}} {
		result, err := s.changeNamedSSHKeyPolicy(args)
		if err != nil || result.V != 1 || result.Applies != "next_start" {
			t.Fatal("policy edit did not report next-start semantics", err)
		}
		if !slices.Equal(agent.names(), []string{"DEPLOY_KEY"}) {
			t.Fatal("policy names replaced the actual key owner")
		}
		if _, err := client.Sign(key.PublicKey(), []byte("still running")); err != nil {
			t.Fatal("next-start edit changed running signing", err)
		}
	}
	if saved.Policy == nil || slices.Contains(saved.Policy.Sandbox.SSHKeys, "DEPLOY_KEY") || !slices.Contains(saved.Policy.Sandbox.SSHKeys, "OTHER_KEY") {
		t.Fatal("next-start name change was not durably published")
	}
	encoded, err := json.Marshal(saved)
	if err != nil || strings.Contains(string(encoded), string(value)) {
		t.Fatal("key material entered the saved metadata")
	}
}
