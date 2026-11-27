package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostRuleReferenceListsOnlyNamedAllowedSections(t *testing.T) {
	text, err := hostRuleConfigSection(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"profiles.\"<name>\".sandbox.host", "repos.\"<origin>\".sandbox.host", "ordered list", "--set", "saved host's layer", "always wins"} {
		if !strings.Contains(text, expected) {
			t.Fatal("the host rule reference is incomplete", expected, text)
		}
	}
	if strings.Contains(text, "| `sandbox.host` |") || strings.Contains(text, "| `host.sandbox.host` |") || strings.Contains(text, "sandbox.host.names") || strings.Contains(text, "sandbox.host.auto") {
		t.Fatal("the reference advertised a forbidden global key or exposed the internal union representation")
	}
}

func TestHostRuleReferenceReadsTheActualSchemaComment(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "internal", "api")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := "package api\n\ntype SandboxValues struct {\n// Host Selects the original named choice.\nHost *HostSelectorV1 `toml:\"host,omitempty\"`\n}\n"
	path := filepath.Join(dir, "layer_values.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := hostRuleConfigSection(repo)
	if err != nil || !strings.Contains(text, "original named choice") {
		t.Fatal("the generated reference ignored the actual schema comment", text, err)
	}
	if err := os.WriteFile(path, []byte("package api\ntype SandboxValues struct{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hostRuleConfigSection(repo); err == nil {
		t.Fatal("a missing production field silently generated an incomplete reference")
	}
}
