//go:build linux

package jail

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

// VMMReadonlyInitPlanV1 contains only the already selected original boot DATA.
// Only its original opaque store source and concrete Cmd can attach this plan.
// It is not a selector, map grant, source factory or native peer identity.
type VMMReadonlyInitPlanV1 struct {
	Program   supply.Item     `json:"program"`
	Kernel    supply.Item     `json:"kernel"`
	Argv      []string        `json:"argv"`
	Runtime   json.RawMessage `json:"runtime"`
	VMDir     string          `json:"vm_dir"`
	DiskMax   int64           `json:"disk_max"`
	MemoryMax int64           `json:"memory_max"`
	API       string          `json:"api"`
	Vsock     string          `json:"vsock"`
}

func (plan *VMMReadonlyInitPlanV1) checkV1(c *setup) error {
	if plan == nil || c == nil || c.Sub == nil || c.VMMExecV1 == nil || !c.VMMExecV1.ReadonlyInit ||
		plan.Program.Kind != supply.KindVMM || plan.Program.Name != "cloud-hypervisor" && plan.Program.Name != "firecracker" ||
		plan.Kernel.Kind != supply.KindKernel || plan.Kernel.Name != "guest" || plan.Kernel.Arch != plan.Program.Arch ||
		len(plan.Runtime) == 0 || len(plan.Runtime) > 64<<10 || !json.Valid(plan.Runtime) || plan.DiskMax <= 0 || plan.MemoryMax <= 0 ||
		len(plan.Argv) < 2 || len(plan.Argv) > 128 || plan.Argv[0] != "/proc/self/fd/3" ||
		!filepath.IsAbs(plan.VMDir) || filepath.Clean(plan.VMDir) != plan.VMDir || len(c.Binds) == 0 || c.Binds[0].Path != plan.VMDir || !c.Binds[0].Write ||
		plan.API != filepath.Join(plan.VMDir, "api.sock") || plan.Vsock != filepath.Join(plan.VMDir, "vsock.sock") {
		return vmmModeFailureV1("the original read-only VMM plan changed its fixed native purpose")
	}
	if c.Sub.SubUID == -1 && (c.Sub.UID < 0 || c.Sub.GID < 0 || c.Sub.SubGID <= 0 || c.Sub.SubGID == c.Sub.GID || c.Sub.NewUIDMap != "" || c.Sub.NewGIDMap == "") {
		return vmmModeFailureV1("the read-only login-UID plan has no actual distinct two-GID admission")
	}
	total := 0
	for _, value := range plan.Argv {
		total += len(value) + 1
		if strings.ContainsRune(value, 0) || total > 64<<10 {
			return vmmModeFailureV1("the original read-only VMM argument is invalid")
		}
	}
	return nil
}

// Namespace IDs are execution data only. The original Cmd separately admits
// the map and proves its exact native uid_map and gid_map on actual C and B.
func readonlyLoginUIDV1(c *setup) bool {
	return c != nil && c.Sub != nil && c.Sub.SubUID == -1 && c.VMMExecV1 != nil && c.VMMExecV1.ReadonlyInit
}

func readonlyExecutionIDsV1(c *setup) (uid, gid int) {
	if readonlyLoginUIDV1(c) {
		return 0, 0
	}
	return 1, 1
}

func readonlyPreexecGroupsV1(c *setup) [4]uint64 {
	if readonlyLoginUIDV1(c) {
		return [4]uint64{0, 1, 0, 1}
	}
	return [4]uint64{1, 0, 1, 0}
}

func (c *Cmd) BindVMMReadonlyInitPlanV1(source *supply.ExecModeLeaseV1, plan VMMReadonlyInitPlanV1) error {
	if c == nil || source == nil {
		return vmmModeFailureV1("the original read-only VMM constructor is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owner := c.modeExecV1
	if c.phase != phaseNew || owner == nil || owner.source != source || !owner.readonly || owner.readonlyPlan != nil {
		return vmmModeFailureV1("the read-only plan belongs to another original Cmd")
	}
	item, err := source.OriginalItemV1(context.WithoutCancel(c.ctx))
	if err != nil {
		return err
	}
	if item != plan.Program {
		return vmmModeFailureV1("the read-only plan names another original signed programme")
	}
	var set setup
	if err := json.Unmarshal(c.setup, &set); err != nil {
		return err
	}
	if err := plan.checkV1(&set); err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	var copy VMMReadonlyInitPlanV1
	if err := json.Unmarshal(raw, &copy); err != nil {
		return err
	}
	set.ReadonlyVMMInitV1 = &copy
	encoded, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode original read-only VMM setup: %w", err)
	}
	group, err := captureReadonlyVMMGroupV1(copy.MemoryMax)
	owner.readonlyGroup = group
	if err != nil {
		return err
	}
	c.setup, owner.readonlyPlan = encoded, &copy
	return nil
}
