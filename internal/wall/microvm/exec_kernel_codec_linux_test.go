//go:build linux

package microvm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

// This proves only closed plan-data preservation, not boot/FD authority.
func TestFinalKernelFSVeritySurvivesCloneAndClosedHelperCodec(t *testing.T) {
	digest := strings.Repeat("a", 64)
	plan := runnerPlan{Version: 1, Spec: wall.Spec{VM: &wall.VMSpec{Runtime: supply.Manifest{Items: []supply.Item{{Kind: supply.KindKernel, Name: "guest", Arch: "amd64", FSVerity: digest}}}}}}
	clone := plan.Spec.VM.Clone()
	clone.Runtime.Items[0].FSVerity = strings.Repeat("b", 64)
	if plan.Spec.VM.Runtime.Items[0].FSVerity != digest {
		t.Fatal("clone rewrote the original kernel pin")
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded runnerPlan
	if err := decodeHelper(bytes.NewReader(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	item, ok := decoded.Spec.VM.Runtime.Find(supply.KindKernel, "guest", "amd64")
	if !ok || item.FSVerity != digest {
		t.Fatal("the final helper lost its original fs-verity check value")
	}
	needle := []byte(`"fsverity":"` + digest + `"`)
	unknown := bytes.Replace(encoded, needle, append(append([]byte(nil), needle...), []byte(`,"measured":true`)...), 1)
	if bytes.Equal(encoded, unknown) {
		t.Fatal("codec fixture did not contain the expected field")
	}
	if err := decodeHelper(bytes.NewReader(unknown), &decoded); err == nil {
		t.Fatal("a caller measurement claim entered the closed item codec")
	}
}
