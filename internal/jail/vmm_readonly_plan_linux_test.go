//go:build linux

package jail

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestReadonlyPlanMetadataCannotIssueOriginalNativeSourceV1(t *testing.T) {
	plan := VMMReadonlyInitPlanV1{Runtime: json.RawMessage(`{"version":3}`)}
	if err := (&Cmd{}).BindVMMReadonlyInitPlanV1(&supply.ExecModeLeaseV1{}, plan); err == nil {
		t.Fatal("metadata/zero source issued a native init")
	}
	if err := (&vmmExecOwnerV1{}).acceptReadonlyNativeV1(vmmNativeMessageV1{}, nil, nil); err == nil {
		t.Fatal("zero native owner accepted readonly messages")
	}
	if _, err := (&supply.ExecModeLeaseV1{}).ReadonlyOriginalV1(context.Background()); err == nil {
		t.Fatal("zero source acquired native readonly origin")
	}
}

func TestReadonlyPlanHasOnlyExistingTwoMapFixedVMMLayoutV1(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vm")
	plan := VMMReadonlyInitPlanV1{Program: supply.Item{Kind: supply.KindVMM, Name: "cloud-hypervisor", Arch: "amd64"}, Kernel: supply.Item{Kind: supply.KindKernel, Name: "guest", Arch: "amd64"}, Argv: []string{"/proc/self/fd/3", "--kernel", "/proc/self/fd/4"}, Runtime: json.RawMessage(`{"version":3}`), VMDir: dir, DiskMax: 4096, MemoryMax: 642 << 20, API: filepath.Join(dir, "api.sock"), Vsock: filepath.Join(dir, "vsock.sock")}
	set := &setup{Sub: &subID{}, VMMExecV1: &vmmExecSetupV1{ReadonlyInit: true}, Binds: []bind{{Path: dir, Write: true}}}
	if err := plan.checkV1(set); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*VMMReadonlyInitPlanV1){func(p *VMMReadonlyInitPlanV1) { p.Program.Name = "runsc" }, func(p *VMMReadonlyInitPlanV1) { p.Kernel.Name = "guest-efi" }, func(p *VMMReadonlyInitPlanV1) { p.API = "/tmp/another.sock" }, func(p *VMMReadonlyInitPlanV1) { p.Argv = []string{"/proc/self/fd/8"} }, func(p *VMMReadonlyInitPlanV1) { p.Runtime = json.RawMessage(`{`) }, func(p *VMMReadonlyInitPlanV1) { p.Argv = []string{"/proc/self/fd/3", strings.Repeat("x", 64<<10)} }} {
		copy := plan
		copy.Argv = append([]string(nil), plan.Argv...)
		change(&copy)
		if err := copy.checkV1(set); err == nil {
			t.Fatal("foreign purpose or unbounded plan accepted")
		}
	}
	set.Sub = nil
	if err := plan.checkV1(set); err == nil {
		t.Fatal("one-ID setup acquired two-map direct-init plan")
	}
}

func TestReadonlyLoginUIDRequiresDistinctGroupAdmissionAndNativeMapV1(t *testing.T) {
	c := &setup{Sub: &subID{UID: 1000, GID: 1000, SubUID: -1, SubGID: 200000, NewGIDMap: "/usr/bin/newgidmap"}, VMMExecV1: &vmmExecSetupV1{ReadonlyInit: true}}
	uid, gid := readonlyExecutionIDsV1(c)
	if uid != 0 || gid != 0 || readonlyPreexecGroupsV1(c) != [4]uint64{0, 1, 0, 1} {
		t.Fatal("mapping admission changed the selected login execution identity")
	}
	if err := checkReadonlyMapRowsV1([]byte("0 1000 1\n"), 1000, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := checkReadonlyMapRowsV1([]byte("0 1000 1\n1 200000 1\n"), 1000, 200000, 2); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"0 1000 1\n", "0 1000 1\n1 200001 1\n", "0 1000 2\n1 200000 1\n", "1 200000 1\n0 1000 1\n", "0 1000 1\n1 200000 1\n2 200001 1\n"} {
		if err := checkReadonlyMapRowsV1([]byte(data), 1000, 200000, 2); err == nil {
			t.Fatal("metadata or a different native map supplied a second GID", data)
		}
	}
	if err := checkReadonlyMapRowsV1([]byte("0 1000 1\n1 1000 1\n"), 1000, 1000, 2); err == nil {
		t.Fatal("duplicate login group pretended to be a genuine second mapping")
	}
	if err := (&Cmd{}).BindVMMReadonlyInitPlanV1(&supply.ExecModeLeaseV1{}, VMMReadonlyInitPlanV1{}); err == nil {
		t.Fatal("a login-UID plan or zero source issued a native owner")
	}
}
