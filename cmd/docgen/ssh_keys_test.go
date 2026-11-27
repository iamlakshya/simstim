package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHKeyReferenceUsesRequestedRealSchemaFieldsOnly(t *testing.T) {
	keys, kind, err := sshKeyConfigFields(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].key != "sandbox.ssh_keys" || keys[0].typ != "array of string" || keys[0].def != "`[]`" || !strings.Contains(keys[0].doc, "next sandbox start") {
		t.Fatalf("SSH key reference lost its source contract: %+v", keys)
	}
	if len(kind) != 1 || kind[0].key != "secrets.<name>.kind" || kind[0].typ != "string" || kind[0].def != "`\"\"`" || !strings.Contains(kind[0].doc, "ssh-key") {
		t.Fatalf("secret kind reference lost its source contract: %+v", kind)
	}
}

func TestSourceOwnedSSHKeyRowsReachConfigurationTables(t *testing.T) {
	page, err := configPage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for key, count := range map[string]int{"sandbox.ssh_keys": 2, "secrets.<name>.kind": 1, "sandbox.image_policy.identity": 2, "sandbox.image_policy.issuer": 2, "sandbox.image_policy.trusted_root": 2} {
		if got := strings.Count(string(page), "| `"+key+"` |"); got != count {
			t.Errorf("configuration reference has %d rows for %s, want %d", got, key, count)
		}
	}
}
