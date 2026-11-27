package api

import (
	"strings"
	"testing"
)

func TestInputEcosystemsRemainBoundToOriginalDeclarationAndOwnTheirSlice(t *testing.T) {
	plan := SessionInputs{Version: SessionInputsV1, Nonce: InputNonce(strings.Repeat("a", 64)), Identity: GitIdentity{Known: true},
		Repositories: []InputRepository{{Name: "fixture", Commit: strings.Repeat("b", 40), Size: 1, SHA256: strings.Repeat("c", 64), ObjectFormat: "sha1", Ecosystems: []string{"go"}}}}
	if err := plan.Validate(DefaultCopyMax, DefaultSeedMax); err != nil {
		t.Fatal(err)
	}
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	owned := plan.Canonical()
	plan.Repositories[0].Ecosystems[0] = "python"
	if owned.Repositories[0].Ecosystems[0] != "go" {
		t.Fatal("authenticated repository tags retained caller storage")
	}
	after, err := plan.Digest()
	if err != nil || before == after {
		t.Fatal("ecosystem facts escaped the input declaration digest")
	}
	plan.Repositories[0].Ecosystems = []string{"unknown"}
	if err := plan.Validate(DefaultCopyMax, DefaultSeedMax); err == nil {
		t.Fatal("an arbitrary ecosystem explanation entered authenticated input metadata")
	}
}
