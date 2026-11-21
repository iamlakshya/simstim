package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestNamedSSHKeyDeliveryHasClosedExactNameDomain(t *testing.T) {
	valid := &NamedSSHKeysV1{Version: 1, Names: []string{"DEPLOY_KEY"}, Values: map[string]Secret{"DEPLOY_KEY": "private-fixture"}}
	if !valid.Valid() {
		t.Fatal("valid closed delivery refused")
	}
	for _, change := range []func(*NamedSSHKeysV1){
		func(k *NamedSSHKeysV1) { k.Version = 2 },
		func(k *NamedSSHKeysV1) { k.Names = append(k.Names, "DEPLOY_KEY") },
		func(k *NamedSSHKeysV1) { k.Names[0] = "SSH_AUTH_SOCK" },
		func(k *NamedSSHKeysV1) { k.Values["OTHER"] = "extra" },
		func(k *NamedSSHKeysV1) { k.Values["DEPLOY_KEY"] = "" },
		func(k *NamedSSHKeysV1) { k.Values["DEPLOY_KEY"] = Secret(strings.Repeat("x", (64<<10)+1)) },
	} {
		candidate := CloneNamedSSHKeysV1(valid)
		change(candidate)
		if candidate.Valid() {
			t.Fatal("invalid delivery accepted")
		}
	}
	clone := CloneNamedSSHKeysV1(valid)
	clone.Names[0] = "OTHER"
	clone.Values["DEPLOY_KEY"] = "changed"
	if valid.Names[0] != "DEPLOY_KEY" || valid.Values["DEPLOY_KEY"] != "private-fixture" {
		t.Fatal("private clone aliases original")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, *valid), "private-fixture") {
			t.Fatal("private key delivery leaked through formatting")
		}
	}
}
