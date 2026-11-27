//go:build linux

package host

import (
	"net"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
)

func TestPrivatePurgeEndpointPreservesReplacedSocketV2(t *testing.T) {
	runtime, err := os.MkdirTemp("/tmp", "pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if err := os.Mkdir(paths.RuntimeDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	endpoint, err := newPurgePrivateEndpointV2()
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	if err := endpoint.Check(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(endpoint.path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	defer replacement.Close()
	info, err := os.Lstat(endpoint.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err == nil {
		t.Fatal("replacement socket was treated as the original endpoint")
	}
	current, err := os.Lstat(endpoint.path)
	if err != nil || !os.SameFile(info, current) {
		t.Fatal("original endpoint cleanup removed the replacement", err)
	}
}
