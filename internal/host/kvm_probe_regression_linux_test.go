//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestKVMProbeDoesNotConfuseARegularFileWithUsableKVM(t *testing.T) {
	agentBridgeEnv(t)
	root := t.TempDir()
	path := filepath.Join(root, "dev", "kvm")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a KVM device"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := probeKVM(capSource{root: root, uid: os.Getuid()}, "/manager-fixture")
	if !probe.Present || probe.Usable || probe.AccessRestricted {
		t.Fatal("a fake file became usable KVM", probe)
	}
	if probeDeviceOpen(path, true) {
		t.Fatal("open success without actual API version12 became KVM readiness")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if probe := probeKVM(capSource{root: root, uid: os.Getuid()}, "/manager-fixture"); probe.Present || probe.Usable {
		t.Fatal("an absent device acquired KVM facts", probe)
	}
}

func TestKVMDeviceIdentityRejectsOtherCharacterDevices(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  uint32
		major uint32
		minor uint32
		want  bool
	}{
		{"native KVM", unix.S_IFCHR, 10, 232, true},
		{"ordinary file", unix.S_IFREG, 10, 232, false},
		{"block device", unix.S_IFBLK, 10, 232, false},
		{"different misc device", unix.S_IFCHR, 10, 231, false},
		{"different major", unix.S_IFCHR, 11, 232, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stat := unix.Stat_t{Mode: test.mode, Rdev: unix.Mkdev(test.major, test.minor)}
			if isKVMDeviceV1(stat) != test.want {
				t.Fatal("device identity did not match the closed native KVM device")
			}
		})
	}
}

func TestKVMProbeRefusesSymlinksOtherDevicesAndUnreadableFiles(t *testing.T) {
	agentBridgeEnv(t)
	root := t.TempDir()
	path := filepath.Join(root, "kvm")
	if err := os.Symlink("/dev/null", path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, "/dev/null", filepath.Join(root, "missing")} {
		if usable, restricted := probeKVMDeviceOpenV1(name); usable || restricted {
			t.Fatalf("%s became KVM or an access-repair promise", name)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unreadable ordinary file"), 0); err != nil {
		t.Fatal(err)
	}
	if usable, restricted := probeKVMDeviceOpenV1(path); usable || restricted {
		t.Fatal("a regular file refusal became a KVM access restriction")
	}
}
