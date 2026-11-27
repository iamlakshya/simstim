package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var baseRequired = []string{
	"VIRTIO_PCI", "VIRTIO_MMIO", "VIRTIO_BLK", "VIRTIO_CONSOLE", "HW_RANDOM_VIRTIO",
	"VIRTIO_BALLOON", "VSOCKETS", "VIRTIO_VSOCKETS",
	"BLK_DEV_DM", "DM_INIT", "DM_VERITY",
	"EROFS_FS", "EXT4_FS", "OVERLAY_FS", "TMPFS", "DEVTMPFS",
	"NF_TABLES", "NFT_REDIR", "CGROUPS", "MEMCG",
	"SECURITY_LANDLOCK", "SECCOMP_FILTER", "USER_NS",
	"FUSE_FS", "VMGENID", "RANDSTRUCT_NONE", "DEBUG_INFO_NONE",
	"NAMESPACES", "NET_NS", "PID_NS", "IPC_NS", "UTS_NS",
	"CGROUP_CPUACCT", "CGROUP_DEVICE", "CGROUP_FREEZER", "CGROUP_SCHED", "CPUSETS",
	"KEYS", "VETH", "BRIDGE", "BRIDGE_NETFILTER",
	"IP_NF_FILTER", "IP_NF_MANGLE", "IP_NF_TARGET_MASQUERADE",
	"IP6_NF_FILTER", "IP6_NF_MANGLE", "IP6_NF_TARGET_MASQUERADE",
	"NETFILTER_XT_MATCH_ADDRTYPE", "NETFILTER_XT_MATCH_CONNTRACK",
	"NETFILTER_XT_MATCH_IPVS", "NETFILTER_XT_MARK",
	"IP_NF_RAW", "IP_NF_NAT", "NF_NAT",
	"IP6_NF_RAW", "IP6_NF_NAT",
	"POSIX_MQUEUE", "CGROUP_BPF",
}

func smallPassingConfig(arch string, overrides map[string]string) string {
	var b strings.Builder
	syms := append([]string(nil), baseRequired...)
	if arch == "amd64" {
		syms = append(syms, "PVH", "ACPI")
	}
	for _, s := range syms {
		v, ok := overrides[s]
		if !ok {
			v = "y"
		}
		switch v {
		case "":
			continue
		case "unset":
			b.WriteString("# CONFIG_" + s + " is not set\n")
		default:
			b.WriteString("CONFIG_" + s + "=" + v + "\n")
		}
	}
	lsm, ok := overrides["LSM"]
	if !ok {
		lsm = `"landlock,yama"`
	}
	if lsm != "" {
		b.WriteString("CONFIG_LSM=" + lsm + "\n")
	}
	for _, f := range []string{"MODULES", "IO_URING", "VSOCKETS_LOOPBACK", "KVM", "IKHEADERS", "LOCALVERSION_AUTO"} {
		if v, ok := overrides[f]; ok {
			b.WriteString("CONFIG_" + f + "=" + v + "\n")
		} else {
			b.WriteString("# CONFIG_" + f + " is not set\n")
		}
	}
	return b.String()
}

func runCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "./check-config.sh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRealConfigsPass(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		cfg := "config-" + arch
		out, err := runCheck(t, cfg, arch)
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", cfg, err, out)
		}
		if out != "" {
			t.Fatalf("%s produced unexpected output: %s", cfg, out)
		}
	}
}

func TestSmallConfigsPassAndFail(t *testing.T) {
	dir := t.TempDir()

	// 1. Small config with spaces in its path passes on both architectures.
	for _, arch := range []string{"amd64", "arm64"} {
		okPath := filepath.Join(dir, "small pass "+arch+".config")
		if err := os.WriteFile(okPath, []byte(smallPassingConfig(arch, nil)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runCheck(t, okPath, arch); err != nil {
			t.Fatalf("small %s should pass: %v\n%s", arch, err, out)
		}
	}

	// 2. Missing required symbol (# CONFIG_DM_INIT is not set, or absent, or =m).
	for _, tc := range []struct {
		name string
		over map[string]string
		want string
	}{
		{
			name: "unset required symbol",
			over: map[string]string{"DM_INIT": "unset"},
			want: "CONFIG_DM_INIT is n in ",
		},
		{
			name: "absent required symbol",
			over: map[string]string{"DM_VERITY": ""},
			want: "CONFIG_DM_VERITY is n in ",
		},
		{
			name: "module required symbol",
			over: map[string]string{"EROFS_FS": "m"},
			want: "CONFIG_EROFS_FS is m in ",
		},
		{
			name: "missing moby symbol",
			over: map[string]string{"CGROUP_BPF": "unset"},
			want: "CONFIG_CGROUP_BPF is n in ",
		},
		{
			name: "missing landlock in LSM",
			over: map[string]string{"LSM": `"lockdown,yama,landlock2"`},
			want: "CONFIG_LSM lacks landlock in ",
		},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".config")
		if err := os.WriteFile(p, []byte(smallPassingConfig("amd64", tc.over)), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runCheck(t, p, "amd64")
		if err == nil {
			t.Errorf("%s: expected failure, got pass", tc.name)
			continue
		}
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "Try:") {
			t.Errorf("%s: unexpected output:\n%s", tc.name, out)
		}
	}

	// 3. Forbidden symbol set to =y or =m fails.
	for _, tc := range []struct {
		sym, val string
	}{
		{"MODULES", "y"},
		{"IO_URING", "y"},
		{"VSOCKETS_LOOPBACK", "y"},
		{"KVM", "m"},
		{"IKHEADERS", "y"},
		{"LOCALVERSION_AUTO", "y"},
	} {
		p := filepath.Join(dir, "forbid-"+tc.sym+".config")
		if err := os.WriteFile(p, []byte(smallPassingConfig("arm64", map[string]string{tc.sym: tc.val})), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runCheck(t, p, "arm64")
		if err == nil {
			t.Errorf("forbidden %s=%s: expected failure, got pass", tc.sym, tc.val)
			continue
		}
		want := "check-config: CONFIG_" + tc.sym + " is " + tc.val + " in " + p + ", and the guest forbids it."
		if !strings.Contains(out, want) || !strings.Contains(out, "Try: Unset it, run make olddefconfig, and commit the config") {
			t.Errorf("forbidden %s=%s: unexpected output:\n%s", tc.sym, tc.val, out)
		}
	}

	// 4. PVH and ACPI are required on amd64 only, not arm64.
	armOnly := filepath.Join(dir, "arm64-no-pvh.config")
	if err := os.WriteFile(armOnly, []byte(smallPassingConfig("arm64", nil)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCheck(t, armOnly, "arm64"); err != nil {
		t.Fatalf("arm64 without PVH/ACPI should pass: %v\n%s", err, out)
	}
	if out, err := runCheck(t, armOnly, "amd64"); err == nil || !strings.Contains(out, "CONFIG_PVH is n") {
		t.Fatalf("amd64 without PVH should fail on CONFIG_PVH, got err=%v out=%s", err, out)
	}

	// 5. Usage / missing file errors carry Try hints.
	for _, args := range [][]string{
		{},
		{armOnly},
		{armOnly, "riscv64"},
		{filepath.Join(dir, "does-not-exist"), "amd64"},
	} {
		out, err := runCheck(t, args...)
		if err == nil || !strings.Contains(out, "Try:") {
			t.Errorf("args %v: expected failure with Try:, got err=%v out=%s", args, err, out)
		}
	}
}
