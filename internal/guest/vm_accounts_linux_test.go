//go:build linux

package guest

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVMAccountsAreFixedAndUnrelatedImageOwnershipIsNotGuessed(t *testing.T) {
	passwd := []byte("root:x:0:0:root:/root:/bin/sh\nservice:x:42:42:service:/srv:/bin/false\n")
	got, err := vmAccountRecords(passwd, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{"service:x:42:42:", "holder:x:900:900:", "agent:x:1000:1000:"} {
		if !strings.Contains(string(got), record) {
			t.Fatal("fixed account ABI or unrelated record changed", string(got))
		}
	}
	groups, err := vmAccountRecords([]byte("root:x:0:\ndocker:x:999:\nservice:x:42:\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(groups), "docker:x:2000:") || !strings.Contains(string(groups), "service:x:42:") {
		t.Fatal("docker ABI was inferred from image group IDs")
	}
}

func TestVMAccountsRejectPrivilegeAndReservedIDConflicts(t *testing.T) {
	for _, input := range []string{"agent:x:0:0::/root:/bin/sh\n", "intruder:x:900:900::/:/bin/sh\n", "agent:x:1000:1000::/home/agent:/bin/sh\nagent:x:1000:1000::/home/agent:/bin/sh\n", "agent\x00hidden:x:0:0::/:/bin/sh\n"} {
		if _, err := vmAccountRecords([]byte(input), false); err == nil {
			t.Fatal("unsafe image account layout normalised silently")
		}
	}
	if _, err := vmAccountRecords([]byte("other:x:2000:\n"), true); err == nil {
		t.Fatal("unrelated image group reused reserved dockerGID")
	}
}

func TestVMDockerDaemonArgumentsNeverSelectHostSocketsOrTCP(t *testing.T) {
	args := vmDockerArgs("/run/guest/docker.sock", "/.persist/docker", "/run/guest/docker-exec")
	joined := strings.Join(args, "\n")
	for _, required := range []string{"--host=unix:///run/guest/docker.sock", "--group=docker", "--data-root=/.persist/docker", "--exec-opt=native.cgroupdriver=cgroupfs"} {
		if !strings.Contains(joined, required) {
			t.Fatal("controlled Docker argument absent", required)
		}
	}
	for _, forbidden := range []string{"tcp://", "/var/run/docker.sock", "--privileged", "--rootless"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("Docker boundary was widened", forbidden)
		}
	}
}

func TestVMImageLoginShellDoesNotChooseCredentials(t *testing.T) {
	data, err := vmAccountRecords([]byte("agent:x:1000:42:image:/other:/bin/zsh\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "agent:x:1000:1000:agent:/home/agent:/bin/zsh\n") {
		t.Fatal("image shell or fixed credential/home ABI lost", string(data))
	}
}

func TestVMAccountReaderRefusesImageSymlinkRatherThanOpeningInitFD(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self/fd/0", filepath.Join(base, "etc/passwd")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := vmReadAccount(root, "etc/passwd"); err == nil {
		t.Fatal("image account path followed a kernel/descriptor alias")
	}
}

func TestVMDockerBridgeAndDNSArgumentsShareOnePrivateTopology(t *testing.T) {
	args := strings.Join(vmDockerArgs("/run/guest/docker.sock", "/.persist/docker", "/run/guest/docker-exec"), "\n")
	for _, required := range []string{"--bip=" + net.IPv4(172, 30, 0, 1).String() + "/24", "--default-address-pool=base=" + (&net.IPNet{IP: net.IPv4(172, 30, 0, 0), Mask: net.CIDRMask(16, 32)}).String() + ",size=24", "--dns=" + net.IPv4(172, 30, 0, 1).String(), "--ipv6=false", "--shutdown-timeout=3"} {
		if !strings.Contains(args, required) {
			t.Fatal("container network/lifecycle configuration missing", required)
		}
	}
}
