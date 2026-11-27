package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestLinuxCapabilityFiles(t *testing.T) {
	s := fakeCapSource(t)
	fakeCapFile(t, s, "/sys/fs/cgroup/cgroup.controllers", "cpu memory pids\n")
	fakeCapFile(t, s, fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/cgroup.controllers", 1000, 1000), "cpu pids\n")
	fakeCapFile(t, s, "/proc/self/status", "Seccomp:\t2\n")
	fakeCapFile(t, s, "/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "1\n")
	fakeCapFile(t, s, "/proc/sys/kernel/io_uring_disabled", "2\n")
	fakeCapFile(t, s, "/etc/modprobe.d/50-"+brand.Name+"-vsock.conf", "blacklist vsock_loopback\nblacklist vmw_vsock_virtio_transport\nblacklist vhost_vsock\n")
	p := api.CapProbe{}
	readPlatformCapabilities(context.Background(), s, &p)
	if !p.CgroupV2 || !p.Seccomp || !p.UserNSRestricted || !p.VsockBlocked || p.IOURingDisabled != 2 || !reflect.DeepEqual(p.Delegated, []string{"cpu", "pids"}) {
		t.Fatalf("linux facts %+v", p)
	}
	if p.KVM.Present || p.KVM.Usable {
		t.Fatal("missing KVM appeared usable")
	}
	fakeCapFile(t, s, "/dev/kvm", "")
	p = api.CapProbe{}
	readPlatformCapabilities(context.Background(), s, &p)
	if !p.KVM.Present || p.KVM.Usable {
		t.Fatal("regular file advertised KVM")
	}
}

func TestLinuxCapabilityModuleAndCPUPools(t *testing.T) {
	isolateCapTest(t)
	if probeVsockBlocked([]byte("blacklist vsock_loopback\nblacklist vmw_vsock_virtio_transport\n")) {
		t.Fatal("missing vhost block accepted")
	}
	if !probeVsockBlocked([]byte("install vsock_loopback /bin/false\ninstall vmw_vsock_virtio_transport /usr/bin/false\ninstall vhost_vsock /bin/false\n")) {
		t.Fatal("install blockers rejected")
	}
	if got := probeCPUList([]byte("0-2,4")); !reflect.DeepEqual(got, []int{0, 1, 2, 4}) {
		t.Fatalf("cpus %v", got)
	}
	for _, input := range []string{"-1", "2-1", "0-999999", "0,x"} {
		if probeCPUList([]byte(input)) != nil {
			t.Errorf("accepted cpu list %q", input)
		}
	}
}

func TestCapabilityDiskProbeStaysInIncoming(t *testing.T) {
	dir := isolateCapTest(t)
	dataDir := filepath.Join(dir, "absent-data")
	probeDisk(dataDir, filepath.Join(dataDir, "artefacts", "incoming"))
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe made data ancestors: %v", err)
	}
	dataDir = filepath.Join(dir, "existing-data")
	artefacts := filepath.Join(dataDir, "artefacts")
	if err := os.MkdirAll(artefacts, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	incoming := filepath.Join(artefacts, "incoming")
	if err := os.Symlink(outside, incoming); err != nil {
		t.Fatal(err)
	}
	_, _, reflink := probeDisk(dataDir, incoming)
	if reflink {
		t.Fatal("probe followed incoming link")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe wrote outside incoming: %v %v", entries, err)
	}
	if err := os.Remove(incoming); err != nil {
		t.Fatal(err)
	}
	probeDisk(dataDir, incoming)
	entries, err = os.ReadDir(incoming)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch files remain: %v %v", entries, err)
	}
}

func TestCapabilitySeccompSupportWithoutNamespaces(t *testing.T) {
	s := fakeCapSource(t)
	fakeCapFile(t, s, "/proc/self/status", "Seccomp:\t0\n")
	s.wallProbes = func(context.Context) []wall.Probe {
		return []wall.Probe{{Driver: "process", Level: wall.Process, OK: false, Facts: map[string]string{"userns": "false", "seccomp": "false", "runtime_available": "false"}}}
	}
	p, err := capabilityProbe(context.Background(), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Seccomp {
		t.Fatal("namespace failure erased independent seccomp support")
	}
	if p.UserNS || len(p.Walls) != 1 || p.Walls[0].OK || p.Walls[0].Facts["seccomp"] != "false" {
		t.Fatalf("driver proof was changed: %+v", p)
	}
}
