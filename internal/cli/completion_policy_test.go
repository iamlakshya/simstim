package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPolicyRuleCompletionUsesActualCommandAndEveryLocalLayer(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	settings := "[network]\nallow = ['all.example.org']\n[repos.\"forge.example/team/*\".network]\nallow = ['repo.example.org']\n[profiles.review.network]\ndeny = ['profile.example.org']\n"
	hosts := "[[host]]\nname = 'box'\nssh = 'unreachable.invalid'\n[host.network]\nallow = ['host.example.org']\n"
	for name, data := range map[string]string{"config.toml": settings, "hosts.toml": hosts} {
		if err := os.WriteFile(filepath.Join(cfg, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for _, rule := range []api.Rule{
		{Layer: api.LayerAll, Door: api.DoorNet, Allow: true, Target: "all.example.org"},
		{Layer: api.LayerHost, Name: "box", Door: api.DoorNet, Allow: true, Target: "host.example.org"},
		{Layer: api.LayerRepo, Name: "forge.example/team/*", Door: api.DoorNet, Allow: true, Target: "repo.example.org"},
		{Layer: api.LayerProfile, Name: "review", Door: api.DoorNet, Target: "profile.example.org"},
	} {
		want = append(want, api.RuleID(rule)+"\t"+rule.Target)
	}
	slices.Sort(want)
	got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "rm", "")
	if !slices.Equal(got, want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatalf("actual rule completion = %q, %d, %q, want %q", got, directive, stderr, want)
	}
	for _, row := range want {
		id, _, _ := strings.Cut(row, "\t")
		got, directive, stderr = execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "rm", id)
		if !slices.Contains(got, row) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
			t.Fatalf("prefix %q lost its exact canonical rule: %q, %d, %q", id, got, directive, stderr)
		}
	}
}

func TestProfileCompletionNeedsOnlyItsLocalConfig(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	name := strings.Repeat("p", 129)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[profiles."+name+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "hosts.toml"), []byte("[[host]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "new", "box", "--profile", "")
	if !slices.Equal(got, []string{name}) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatalf("local profile completion = %q, %d, %q", got, directive, stderr)
	}
	got, directive, stderr = execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "rm", "")
	if len(got) != 0 || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatalf("broken rule metadata produced values or noise: %q, %d, %q", got, directive, stderr)
	}
}

func TestRuleCompletionAllowsAnAbsentHostsFile(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[network]\nallow = ['only.example.org']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rule := api.Rule{Layer: api.LayerAll, Door: api.DoorNet, Allow: true, Target: "only.example.org"}
	got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "rm", "")
	if !slices.Equal(got, []string{api.RuleID(rule) + "\t" + rule.Target}) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatalf("one local layer: %q, %d, %q", got, directive, stderr)
	}
}

func TestPolicyRuleCompletionKeepsDistinctTargetsWithOneStableID(t *testing.T) {
	cfg, _ := isolateCompletionEnv(t)
	targets := []string{"collision-6295.example", "collision-6343.example"}
	const id = "gcjvwfw"
	for _, target := range targets {
		if got := api.RuleID(api.Rule{Layer: api.LayerAll, Door: api.DoorNet, Allow: true, Target: target}); got != id {
			t.Fatalf("the legal collision fixture changed: %s has ID %s", target, got)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[network]\nallow = ['"+targets[0]+"', '"+targets[1]+"']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "rm", id)
	want := []string{id + "\t" + targets[0], id + "\t" + targets[1]}
	if !slices.Equal(got, want) || directive != cobra.ShellCompDirectiveNoFileComp || stderr != "" {
		t.Fatalf("distinct colliding rules disappeared: %q, %d, %q, want %q", got, directive, stderr, want)
	}
}

func TestRuleCompletionCollapsesLeaseRowsWithoutHidingCollidingTargets(t *testing.T) {
	const id = "gcqjkxl"
	first := api.Rule{Layer: api.LayerSession, Door: api.DoorNet, Allow: true, Target: "session-collision-7608.example", Once: true}
	first.ID = api.RuleID(first)
	lease := first
	lease.Once = false
	lease.Until = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	other := api.Rule{Layer: api.LayerSession, Door: api.DoorNet, Allow: true, Target: "session-collision-21481.example"}
	other.ID = api.RuleID(other)
	if first.ID != id || api.RuleID(lease) != id || other.ID != id {
		t.Fatal("the actual ID/lease/collision contract changed")
	}
	want := []string{id + "\t" + other.Target, id + "\t" + first.Target}
	slices.Sort(want)
	if got := completionRuleRows([]api.Rule{first, lease, first, other}, id); !slices.Equal(got, want) {
		t.Fatalf("lease rows duplicated an ID or hid a colliding target: %q, want %q", got, want)
	}
}
