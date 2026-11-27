package agents

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSecretPrefixUsesSelectedNormativeRowAndFilteredGlobalFallback(t *testing.T) {
	for _, tc := range []struct{ kind, name, want string }{
		{"claude", "ANTHROPIC_API_KEY", "sk-ant-api03-"},
		{"claude", "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-"},
		{"shell", "ANTHROPIC_API_KEY", ""},
		{"shell", "CLAUDE_CODE_OAUTH_TOKEN", ""},
		{"custom", "CLAUDE_CODE_OAUTH_TOKEN", ""},
		{"codex", "OPENAI_API_KEY", ""},
		{"custom", "UNCONFIGURED", ""},
	} {
		got, err := SecretPrefixForAgent(tc.kind, tc.name)
		if err != nil || got != tc.want {
			t.Fatal("secret prefix gained unverified global inference or lost selected normative data")
		}
	}
}

func TestSecretPrefixRefusesDuplicateSelectedMetadata(t *testing.T) {
	before := Table
	Table = []Agent{{Kind: "fixture", Secrets: []api.AgentSecretBinding{{Env: "KEY", Prefix: "one"}, {Env: "KEY", Prefix: "two"}}}}
	t.Cleanup(func() { Table = before })
	if _, err := SecretPrefixForAgent("fixture", "KEY"); err == nil {
		t.Fatal("ambiguous selected prefix was silently chosen")
	}
	if got, err := SecretPrefixForAgent("unknown", "KEY"); err != nil || got != "" {
		t.Fatal("incomplete ambiguous metadata became a global prefix")
	}
}

func TestSavedSecretPrefixIsPublicProvenanceAndNeverASelectedDefault(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		valid        bool
	}{
		{"CUSTOM_KEY", "", true}, {"CUSTOM_KEY", "AB", false},
		{"CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-", true},
		{"ANTHROPIC_API_KEY", "sk-ant-api03-", true},
		{"OTHER_KEY", "sk-ant-oat01-", false},
		{"CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-private-observed", false},
	} {
		got, err := ValidSavedSecretPrefix(tc.name, tc.prefix)
		if err != nil || got != tc.valid {
			t.Fatal("saved public prefix was invented or lost its exact same-name provenance")
		}
	}
	// A legitimate prior joined member can validate its public prefix while
	// the shell's fresh selection must still receive no unverified fallback.
	if fresh, err := SecretPrefixForAgent("shell", "CLAUDE_CODE_OAUTH_TOKEN"); err != nil || fresh != "" {
		t.Fatal("saved provenance broadened fresh selected-agent inference")
	}
}
