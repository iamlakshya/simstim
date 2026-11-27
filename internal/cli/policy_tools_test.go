package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestPolicyToolsRejectInvalidScopeBeforeHostLookup(t *testing.T) {
	cliPolicyHome(t)
	for _, args := range [][]string{
		{"policy", "allow", "api.openai.com", "--in", "missing/abc234de", "--tools", ""},
		{"policy", "allow", "api.openai.com", "--all", "--tools", "files"},
		{"policy", "allow", "api.openai.com", "--tools", "files", "--no-input"},
		{"policy", "allow", "api.openai.com", "--in", "missing/abc234de", "--tools", "unknown"},
		{"policy", "allow", "*.openai.com", "--in", "missing/abc234de", "--tools", "files"},
		{"policy", "allow", "api.openai.com:80", "--in", "missing/abc234de", "--tools", "files"},
		{"policy", "allow", "api.openai.com/v1/chat", "--in", "missing/abc234de", "--tools", "files"},
		{"policy", "deny", "api.openai.com", "--in", "missing/abc234de", "--tools", "files"},
	} {
		root := Root()
		root.SetArgs(args)
		err := root.ExecuteContext(client.WithoutInput(t.Context()))
		var user *client.UserError
		if !errors.As(err, &user) || user.Kind != client.KindUsage || user.Hint == "" {
			t.Fatalf("%v bypassed argument validation: %v", args, err)
		}
	}
}

func TestPolicyToolsDryRunUsesCanonicalSetAndActualFeatureGate(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "old helper", true: "supported"}[supported], func(t *testing.T) {
			const sessionID = "abc234de"
			host, transport := fakeBox(t, []client.Session{{ID: sessionID, Name: "work", State: api.StateIdle}})
			for _, key := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
				t.Setenv(key, t.TempDir())
			}
			transport.HostID = strings.Repeat("a", 32)
			if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, transport.HostID, ""); err != nil {
				t.Fatal(err)
			}
			transport.Features = []string{"policy"}
			if supported {
				transport.Features = append(transport.Features, api.FeatureModelTools)
			}
			transport.Answers = map[string]any{"policy.ls": api.Policy{}}
			root := Root()
			root.SetArgs([]string{"policy", "allow", "api.openai.com", "--in", host.Name + "/" + sessionID, "--tools", "web_search,files", "--tools", "web_search", "--dry-run", "--json"})
			var runErr error
			out, stderr := captureOutput(t, func() { runErr = root.ExecuteContext(client.WithoutInput(t.Context())) })
			for _, op := range transport.Ops() {
				if strings.HasPrefix(op, "policy.") && op != "policy.ls" {
					t.Fatalf("preview sent mutation %q", op)
				}
			}
			if !supported {
				var user *client.UserError
				if !errors.As(runErr, &user) || user.Kind != client.KindRefused || out != "" {
					t.Fatalf("old helper preview widened tools: %q %v", out, runErr)
				}
				return
			}
			if runErr != nil || stderr != "" {
				t.Fatalf("supported preview: %v, stderr %q", runErr, stderr)
			}
			var result struct {
				V      int      `json:"v"`
				Rule   api.Rule `json:"rule"`
				DryRun bool     `json:"dry_run"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if result.V != 1 || !result.DryRun || result.Rule.Layer != api.LayerSession || !reflect.DeepEqual(result.Rule.Tools, []string{"files", "web_search"}) || result.Rule.ID != api.RuleID(result.Rule) {
				t.Fatalf("preview lost canonical optional tools/session identity: %+v", result)
			}
		})
	}
}

func TestPolicyToolsCompleteTheCanonicalTableOnTheActualCommand(t *testing.T) {
	isolateCompletionEnv(t)
	for _, row := range []struct {
		prefix string
		want   []string
	}{
		{"", api.ModelToolNames()},
		{"files,web", []string{"files,web_fetch", "files,web_search"}},
	} {
		got, directive, stderr := execComplete(t, nil, cobra.ShellCompRequestCmd, "policy", "allow", "api.openai.com", "--in", "missing/abc234de", "--tools", row.prefix)
		if !slices.Equal(got, row.want) || directive != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace || stderr != "" {
			t.Fatalf("actual tools completion %q: %q, %d, %q, want %q", row.prefix, got, directive, stderr, row.want)
		}
	}
}
