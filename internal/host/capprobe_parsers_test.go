package host

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func isolateCapTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(dir, key)
		if key == "XDG_CONFIG_DIRS" || key == "XDG_DATA_DIRS" {
			second := path + "-second"
			if err := os.MkdirAll(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, path+string(os.PathListSeparator)+second)
			continue
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	return dir
}

func capFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "capprobe", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCapabilityParsers(t *testing.T) {
	isolateCapTest(t)
	if got := probeOSRelease(capFixture(t, "os-release")); got != "Example Linux 13 (stable)" {
		t.Fatalf("distro %q", got)
	}
	if got := probeCPUVirt(capFixture(t, "cpuinfo"), "amd64"); got != "Intel VT-x" {
		t.Fatalf("CPU %q", got)
	}
	if got := probeCPUVirt([]byte("flags : svm vmxx\n"), "amd64"); got != "AMD-V" {
		t.Fatalf("AMD CPU %q", got)
	}
	if got := probeCPUVirt([]byte("flags : vmxx\n"), "amd64"); got != "" {
		t.Fatalf("false CPU flag %q", got)
	}
	if got := probeCPUVirt(capFixture(t, "cpuinfo"), "arm64"); got != "" {
		t.Fatalf("Arm CPU %q", got)
	}
	want := []string{"cpu", "cpuset", "hugetlb", "io", "memory", "misc", "pids", "rdma"}
	if got := probeControllers(capFixture(t, "cgroup.controllers")); !reflect.DeepEqual(got, want) {
		t.Fatalf("controllers %q", got)
	}
	if got := probeControllers([]byte("memory cpu memory bad!")); !reflect.DeepEqual(got, []string{"cpu", "memory"}) {
		t.Fatalf("bad controllers %q", got)
	}
}

func TestCapabilityACLParser(t *testing.T) {
	isolateCapTest(t)
	fixture := capFixture(t, "acl-xattr")
	if !probeACLUser(fixture, 1000) || probeACLUser(fixture, 1001) {
		t.Fatal("named ACL user")
	}
	for _, size := range []int{0, 1, 3, 5, len(fixture) - 1} {
		if probeACLUser(fixture[:size], 1000) {
			t.Errorf("accepted truncated ACL %d", size)
		}
	}
	bad := append([]byte(nil), fixture...)
	binary.LittleEndian.PutUint32(bad, 3)
	if probeACLUser(bad, 1000) {
		t.Error("accepted unknown ACL version")
	}
	bad = append([]byte(nil), fixture...)
	binary.LittleEndian.PutUint16(bad[4+3*8+2:], 4)
	if probeACLUser(bad, 1000) {
		t.Error("ignored read-only mask")
	}
	if probeACLUser(fixture[:4+3*8], 1000) {
		t.Error("accepted named entry without mask")
	}
}

func TestCapabilityFileValues(t *testing.T) {
	isolateCapTest(t)
	if got := probeOSRelease([]byte("NAME='Example'\nVERSION_ID=13\n")); got != "Example 13" {
		t.Fatalf("fallback distro %q", got)
	}
	if !probeSubID([]byte("example:100000:65536\n"), "example", 1000) {
		t.Error("missing subordinate range")
	}
	if probeSubID([]byte("example:4294901761:65536\n"), "example", 1000) {
		t.Error("overflow subordinate range")
	}
	if probeSubID([]byte("example:100000:1\n"), "example", 1000) {
		t.Error("small subordinate range")
	}
	if got := probeMemTotal([]byte("MemTotal: 4096 kB\n")); got != 4<<20 {
		t.Fatalf("memory %d", got)
	}
	if probeMemTotal([]byte("MemTotal: 9223372036854775807 kB\n")) != 0 {
		t.Error("overflow memory")
	}
	if !probeStatusSeccomp([]byte("Seccomp:\t0\n")) || probeStatusSeccomp([]byte("Seccomp:\t99\n")) {
		t.Error("seccomp support")
	}
	if got := probeStatusGroups([]byte("Groups:\t10 42\n")); !reflect.DeepEqual(got, []string{"10", "42"}) {
		t.Fatalf("groups %q", got)
	}
}
