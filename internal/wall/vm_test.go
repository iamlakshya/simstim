package wall

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestExpectedVMPlanDoesNotAliasCallerOrReturnedSlices(t *testing.T) {
	spec := Spec{ID: "abcdef23", Mounts: []Mount{{Host: "/original"}}, VM: &VMSpec{Version: 1, Runtime: supply.Manifest{Items: []supply.Item{{SHA256: "client"}}, Advisories: []supply.Advisory{{Name: "original"}}}}}
	ctx := WithExpectedSpec(context.Background(), spec)
	spec.Mounts[0].Host = "/changed"
	spec.VM.Runtime.Items[0].SHA256 = "host"
	spec.VM.Runtime.Advisories[0].Name = "changed"
	got, ok := ExpectedSpec(ctx)
	if !ok || got.Mounts[0].Host != "/original" || got.VM.Runtime.Items[0].SHA256 != "client" || got.VM.Runtime.Advisories[0].Name != "original" {
		t.Fatal("expected authority aliases mutable caller state")
	}
	got.VM.Runtime.Items[0].SHA256 = "second change"
	again, _ := ExpectedSpec(ctx)
	if again.VM.Runtime.Items[0].SHA256 != "client" {
		t.Fatal("returned plan aliases stored authority")
	}
}
