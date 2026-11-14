package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func cliPolicyHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyLayerFlagContractsAndTemporaryScope(t *testing.T) {
	cliPolicyHome(t)
	for _, args := range [][]string{
		{"policy", "allow", "api.example.com", "--all", "--for", "1h"},
		{"policy", "allow", "api.example.com", "--profile", "review", "--once"},
		{"policy", "deny", "api.example.com", "--all", "--once"},
		{"policy", "allow", "api.example.com", "--all", "--repo", "."},
	} {
		root := Root()
		root.SetArgs(args)
		err := root.ExecuteContext(context.Background())
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Kind != client.KindUsage {
			t.Fatalf("%v => %v", args, err)
		}
	}
	root := Root()
	root.SetArgs([]string{"policy", "allow", "api.example.com", "--no-input"})
	err := root.ExecuteContext(context.Background())
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "layer_missing" || ue.Kind != client.KindNeedsAnswer {
		t.Fatalf("missing layer = %v", err)
	}
}

func TestPolicyMutationsAndNewRefuseBrokenSettingsBeforeSideEffects(t *testing.T) {
	cliPolicyHome(t)
	path := filepath.Join(client.ConfigDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"new", "unknown", "--detach"}, {"resume", "unknown/session", "--detach"}, {"policy", "allow", "api.example.com", "--all"}, {"policy", "deny", "api.example.com", "--all"}, {"policy", "rm", "g123456", "--all"}} {
		root := Root()
		root.SetArgs(args)
		var stderr bytes.Buffer
		root.SetErr(&stderr)
		err := root.ExecuteContext(context.Background())
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Kind != client.KindUsage || !strings.Contains(ue.Hint, "config check") {
			t.Fatalf("%v = %v", args, err)
		}
		if strings.Contains(stderr.String(), "Warning:") {
			t.Fatalf("mutation printed a defaults warning: %s", stderr.String())
		}
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("refused command wrote a lock")
	}
}

func TestPolicyHelpUsesCanonicalGroupingAndThreeExamples(t *testing.T) {
	root := Root()
	policy, _, err := root.Find([]string{"policy"})
	if err != nil {
		t.Fatal(err)
	}
	if policy.GroupID != "policy" {
		t.Fatal("policy is outside canonical help groups")
	}
	for _, word := range []string{"built-in", "all sessions", "host", "repository", "profile", "session", "locks"} {
		if !strings.Contains(policy.Long, word) {
			t.Fatalf("policy help lacks %s", word)
		}
	}
	if strings.Count(policy.Long, " policy ") < 3 {
		t.Fatal("policy help lacks three examples")
	}
	for _, name := range []string{"profile", "set", "task"} {
		new, _, _ := root.Find([]string{"new"})
		if new.Flags().Lookup(name) == nil {
			t.Fatalf("new lacks --%s", name)
		}
	}
	if api.RuleID(api.Rule{Layer: api.LayerAll, Allow: true, Target: "api.example.com"}) == "" {
		t.Fatal("file ID missing")
	}
}

func TestListWithBrokenSettingsWarnsOnceAndStillRuns(t *testing.T) {
	cliPolicyHome(t)
	path := filepath.Join(client.ConfigDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := Root()
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs([]string{"ls", "--no-input"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr.String(), "Warning:") != 1 || !strings.Contains(stderr.String(), "config check") {
		t.Fatalf("warning=%q", stderr.String())
	}
}
