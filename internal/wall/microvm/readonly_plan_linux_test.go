//go:build linux

package microvm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReadonlyInitCompilesSameSelectedPlanWithoutRuntimeSelectionV1(t *testing.T) {
	for _, driver := range []string{"chv", "firecracker"} {
		t.Run(driver, func(t *testing.T) {
			spec := vmFixture(t)
			if driver == "firecracker" {
				spec = firecrackerFixture(t)
			}
			plan := runnerPlan{Version: 3, Spec: spec, BootUntil: time.Now().Add(time.Minute), UIDMode: "subuid", SessionDirect: true}
			compiled, err := readonlyInitPlanV1(plan)
			if err != nil {
				t.Fatal(err)
			}
			if compiled.Argv[0] != "/proc/self/fd/3" || compiled.Program != spec.VM.Runtime.Items[0] || compiled.Kernel != spec.VM.Runtime.Items[1] || compiled.DiskMax != spec.DiskMax {
				t.Fatal("compiler selected other bytes or disk dimensions")
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(compiled.Runtime, encoded) {
				t.Fatal("the actual original plan was replaced")
			}
			joined := strings.Join(compiled.Argv, "\x00")
			if driver == "chv" && !strings.Contains(joined, "/proc/self/fd/4") {
				t.Fatal("the original kernel descriptor disappeared")
			}
			plan.UIDMode = "login"
			if _, err := readonlyInitPlanV1(plan); err != nil {
				t.Fatal("login execution data was confused with actual native map admission", err)
			}
			plan.UIDMode = "another"
			if _, err := readonlyInitPlanV1(plan); err == nil {
				t.Fatal("unknown execution data entered the fixed purpose")
			}
		})
	}
}
