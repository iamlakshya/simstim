package api

import (
	"encoding/json"
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

func TestCloneNamedSSHKeyViewV1MarshalsEmptySlicesAsArrays(t *testing.T) {
	raw, err := json.Marshal(CloneNamedSSHKeyViewV1(&NamedSSHKeyViewV1{V: 1}))
	if err != nil {
		t.Fatalf("marshal empty view: %v", err)
	}
	if want := `{"v":1,"installed":[],"next_start":[]}`; string(raw) != want {
		t.Fatalf("json = %s, want %s", raw, want)
	}
}
