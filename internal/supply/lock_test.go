package supply

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedSupplyLock(t *testing.T) {
	lock, err := LoadLockFile("../../guest/supply.lock")
	if err != nil {
		t.Fatalf("LoadLockFile(guest/supply.lock): %v", err)
	}
	if !strings.HasPrefix(lock.Kernel.Version, "6.18.") {
		t.Errorf("kernel version = %q, want 6.18.x", lock.Kernel.Version)
	}
	if lock.ReleasedCheck(t); len(lock.VMMs) == 0 {
		t.Fatal("expected at least one [[vmm]]")
	}
	m := &Manifest{Release: "0.6.0", Advisories: KnownBad()}
	if found := m.Matches(AdvisoryKernel, "guest", lock.Kernel.Version); len(found) > 0 {
		t.Errorf("pinned kernel %s matches known-bad list: %+v", lock.Kernel.Version, found)
	}
	for _, v := range lock.VMMs {
		if found := m.Matches(AdvisoryVMM, v.Name, v.Version); len(found) > 0 {
			t.Errorf("pinned VMM %s %s matches known-bad list: %+v", v.Name, v.Version, found)
		}
	}
}

func (l *Lock) ReleasedCheck(t *testing.T) {
	t.Helper()
	if !isDate(l.Kernel.ReleasedDate()) {
		t.Errorf("kernel released = %q", l.Kernel.ReleasedDate())
	}
	for _, v := range l.VMMs {
		if !isDate(v.ReleasedDate()) || !isDate(v.EOLDate()) {
			t.Errorf("vmm %s dates = %q / %q", v.Name, v.ReleasedDate(), v.EOLDate())
		}
	}
}

func TestLoadLockRefusesBadTablesAndPinMismatch(t *testing.T) {
	raw, err := os.ReadFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	good := string(raw)

	cases := []struct {
		name, old, repl, want string
	}{
		{"wrong version", "version = 1", "version = 2", "version 2 is not 1"},
		{"unknown key", "version = 1", "version = 1\nextra = true", `unknown key "extra"`},
		{"kernel pin disagrees", `ref = "v6.18.65"`, `ref = "v6.18.64"`, `[kernel] says version "6.18.65", and [[pin]] "kernel" says ref "v6.18.64"`},
		{"erofs pin disagrees", `ref = "v1.9.4"`, `ref = "v1.9.3"`, `[erofs_utils] says version "1.9.4", and [[pin]] "erofs-utils" says ref "v1.9.3"`},
		{"vmm pin disagrees", `ref = "v54.1"`, `ref = "v54.0"`, `[[vmm]] cloud-hypervisor says version "54.1", and [[pin]] "cloud-hypervisor" says ref "v54.0"`},
		{"missing kernel pin", `name = "kernel"`, `name = "other-kernel"`, `[kernel] has no [[pin]] named "kernel"`},
		{"old erofs", `ref = "v1.9.4"`, `ref = "v1.9.2"`, `says ref "v1.9.2"`},
		{"unpinned builder", "@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6", ":13", "[builder] image"},
		{"bad moby commit", `commit = "70efc0e31da3a115fc87574d1b786fa2b867c346"`, `commit = "70efc0e"`, "[moby_check_config] commit"},
		{"vmm zero size", "size = 7344872", "size = 0", "needs size > 0"},
		{"kernel url mismatch", "linux-6.18.65.tar.xz", "linux-6.18.64.tar.xz", "does not end with /linux-6.18.65.tar.xz"},
	}
	for _, tc := range cases {
		mut := strings.Replace(good, tc.old, tc.repl, 1)
		if mut == good {
			t.Fatalf("%s: %q not found in guest/supply.lock", tc.name, tc.old)
		}
		_, err := LoadLock([]byte(mut))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got err=%v, want %q", tc.name, err, tc.want)
		}
	}

	// Specifically check erofs < 1.9.3 when both pin and table match.
	oldErofs := strings.Replace(good, `ref = "v1.9.4"`, `ref = "v1.9.2"`, 1)
	oldErofs = strings.Replace(oldErofs, `version = "1.9.4"`, `version = "1.9.2"`, 1)
	if _, err := LoadLock([]byte(oldErofs)); err == nil || !strings.Contains(err.Error(), "below 1.9.3") {
		t.Errorf("erofs 1.9.2: got %v, want below 1.9.3", err)
	}
}

func TestCheckPinnedSHA256AndAssetNames(t *testing.T) {
	if got := KernelAsset("amd64"); got != "vmlinux-amd64" {
		t.Errorf("KernelAsset(amd64) = %q", got)
	}
	if got := KernelAsset("arm64"); got != "Image-arm64" {
		t.Errorf("KernelAsset(arm64) = %q", got)
	}
	if got := ToolsAsset("amd64"); got != "tools-amd64.erofs" {
		t.Errorf("ToolsAsset(amd64) = %q", got)
	}
	if got := ConfigAsset("arm64"); got != "config-arm64" {
		t.Errorf("ConfigAsset(arm64) = %q", got)
	}
	if got := KernelSourceAsset("6.18.63"); got != "linux-6.18.63.tar.xz" {
		t.Errorf("KernelSourceAsset = %q", got)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "input.tar.xz")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := CheckPinnedSHA256(p, helloSHA); err != nil {
		t.Fatalf("CheckPinnedSHA256 valid: %v", err)
	}
	var me *MismatchError
	if err := CheckPinnedSHA256(p, strings.Repeat("0", 64)); !errors.As(err, &me) || me.What != "SHA-256" {
		t.Fatalf("CheckPinnedSHA256 mismatch: %v", err)
	}
}
