//go:build linux

package gvisor

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestOCIUsesFreshHomeAndOnlyTypedProcessMounts(t *testing.T) {
	plan := []wall.Mount{{Host: "/usr", Guest: "/usr"}, {Host: "/private/work", Guest: "/work", Write: true}, {Host: "/private/state", Guest: "/home/agent/.cache", Write: true}}
	oci, err := makeOCI(wall.Spec{Root: wall.Reset}, plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if !oci.Root.Readonly || oci.Root.Path != "rootfs" || oci.Process.Terminal || !oci.Process.NoNewPrivileges || oci.Process.User.UID != 1000 || oci.Process.User.GID != 1000 || len(oci.Linux.Namespaces) != 0 {
		t.Fatalf("unsafe OCI boundary: %+v", oci)
	}
	for _, mount := range oci.Mounts {
		if mount.Destination == "/run" && (!slices.Contains(mount.Options, "uid=1000") || !slices.Contains(mount.Options, "gid=1000")) {
			t.Fatal("private writable filesystem does not belong to the virtual guest user", mount)
		}
		if mount.Destination == "/home/agent" && (mount.Type != "bind" || mount.Source != "/home/agent" || !slices.Contains(mount.Options, "rbind") || !slices.Contains(mount.Options, "rw")) {
			t.Fatal("home lost its actual fresh A/gofer backing role", mount)
		}
		if mount.Destination == "/usr" && !slices.Contains(mount.Options, "ro") {
			t.Fatal("system is writable")
		}
		if mount.Destination == "/work" && !slices.Contains(mount.Options, "rw") {
			t.Fatal("workspace is not writable")
		}
	}
	data, err := json.Marshal(oci)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hooks", "uidMappings", "gidMappings", "SSH_AUTH_SOCK", "GVISOR_SIDECAR_BINARIES_DIR"} {
		if strings.Contains(string(data), name) {
			t.Fatal("host bootstrap authority leaked into OCI", name)
		}
	}
}

func TestRunscArgumentsFixNetworkAndDescriptorLink(t *testing.T) {
	args := runArguments("/proc/self/fd/3", "/private/session", "session", 4)
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--network=none", "--directfs=false", "--ignore-cgroups", "--host-uds=none", "--pass-fd 4:3"} {
		if !strings.Contains(joined, flag) {
			t.Fatal("missing required isolation argument", flag)
		}
	}
	for _, forbidden := range []string{"rootless", "host-uds=open", "network=host", "test-only", "TESTONLY"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("unsafe argument", forbidden)
		}
	}
}

func TestOCIRefusesImageAndPrivateFilesystemSubstitution(t *testing.T) {
	if _, err := makeOCI(wall.Spec{Image: &wall.ImageRef{}}, nil, false); err == nil {
		t.Fatal("accepted image filesystem")
	}
	for _, guest := range []string{"/", "/proc/self", "/dev", "/tmp/foo", "/run/foo", "/home", "/home/agent"} {
		if _, err := makeOCI(wall.Spec{}, []wall.Mount{{Host: "/private/source", Guest: guest}}, false); err == nil {
			t.Fatal("accepted replacement", guest)
		}
	}
}

func TestKernelFloor(t *testing.T) {
	for _, tc := range []struct {
		release string
		allowed bool
	}{{"5.5.99", false}, {"5.6.0", true}, {"6.12.1-host", true}, {"invalid", false}, {"5.6", true}} {
		if supportsKernel(tc.release) != tc.allowed {
			t.Fatal(tc)
		}
	}
}
