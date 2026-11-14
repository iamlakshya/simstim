package agents

import (
	"slices"
	"testing"
)

func TestGlobalSecretDefaultsRequireCanonicalUnanimityAndIgnoreStore(t *testing.T) {
	first := SecretBinding{Env: "KEY", Hosts: []string{"API.example.com."}, Header: "authorization", Format: "Bearer {value}", Prefix: "pk_", Source: "https://example.com/auth", Store: []string{"first-store"}}
	second := first
	second.Hosts, second.Header, second.Source, second.Store = []string{"api.example.com:443"}, "Authorization", "https://example.com/other", []string{"different-store"}
	table := []Agent{{Kind: "first", Secrets: []SecretBinding{first}}, {Kind: "second", Secrets: []SecretBinding{second}}}
	got, err := globalSecretDefaults(table)
	if err != nil || len(got) != 1 || got["KEY"].Prefix != "pk_" || !slices.Equal(got["KEY"].Bind.Hosts, []string{"api.example.com:443"}) {
		t.Fatalf("unanimous local metadata failed: %v", err)
	}
	got["KEY"].Bind.Hosts[0] = "changed.example.com:443"
	if first.Hosts[0] != "API.example.com." || second.Hosts[0] != "api.example.com:443" {
		t.Fatal("global default view aliases canonical table data")
	}
}

func TestGlobalSecretDefaultsNeverGuessAnAmbiguousOrMissingField(t *testing.T) {
	base := SecretBinding{Env: "KEY", Hosts: []string{"api.example.com"}, Header: "Authorization", Format: "Bearer {value}", Prefix: "pk_", Source: "https://example.com/auth"}
	for _, mutate := range []func(*SecretBinding){
		func(row *SecretBinding) { row.Hosts = []string{"other.example.com"} },
		func(row *SecretBinding) { row.Header = "X-Api-Key" },
		func(row *SecretBinding) { row.Format = "{value}" },
		func(row *SecretBinding) { row.Prefix = "different_" },
		func(row *SecretBinding) { row.Source = "" },
		func(row *SecretBinding) { row.Header = "" },
	} {
		other := base
		mutate(&other)
		got, err := globalSecretDefaults([]Agent{{Kind: "first", Secrets: []SecretBinding{base}}, {Kind: "second", Secrets: []SecretBinding{other}}})
		if err != nil || len(got) != 0 {
			t.Fatal("global fallback inferred an ambiguous or absent field")
		}
	}
}

func TestSelectedFirstClassOAuthDataDoesNotBecomeGlobalInference(t *testing.T) {
	selected, ok := Find("claude")
	if !ok {
		t.Fatal("first-class data is absent")
	}
	found := false
	for _, row := range selected.SecretBindings() {
		if row.Env == "CLAUDE_CODE_OAUTH_TOKEN" {
			found = row.Header == "Authorization" && row.Format == "Bearer {value}" && row.Prefix == "sk-ant-oat01-"
		}
	}
	if !found {
		t.Fatal("normative selected-agent data was replaced by an omitted code path")
	}
	global, err := GlobalSecretDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, inferred := global["CLAUDE_CODE_OAUTH_TOKEN"]; inferred {
		t.Fatal("a provisional selected-agent header became a global default")
	}
	if known, ok := global["ANTHROPIC_API_KEY"]; !ok || known.Bind.Header != "X-Api-Key" || known.Prefix != "" {
		t.Fatal("verified header or unverified global prefix distinction changed")
	}
}
