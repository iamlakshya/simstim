package cli

import (
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestPolicyLayersGolden(t *testing.T) {
	paste := "media"
	policy, _, err := egress.MergeLayers([]api.Layer{
		{Kind: api.LayerAll, Lock: &api.Lock{Deny: []string{"paste.example.net"}, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: &paste}}}},
		{Kind: api.LayerHost, Name: "box", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"TEAM"}}}},
		{Kind: api.LayerRepo, Name: "forge.example/team/app", Rules: []api.Rule{{Allow: true, Target: "api.example.com"}}},
		{Kind: api.LayerProfile, Name: "review", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"PROFILE"}}}},
		{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "once.example.com", Once: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := captureOutput(t, func() { printPolicy(client.Session{Wall: &api.WallInfo{Level: "process"}}, policy) })
	want, err := os.ReadFile("testdata/policy_layers.golden")
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Fatalf("policy output changed:\n%s\nwant:\n%s", out, want)
	}
}
