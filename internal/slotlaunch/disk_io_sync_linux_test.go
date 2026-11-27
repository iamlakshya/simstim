//go:build linux

package slotlaunch

import (
	"strings"
	"testing"
)

func TestFixedCHVDisksSelectPinnedSynchronousEngine(t *testing.T) {
	request := validStart()
	request.Driver = "chv"
	args, _, err := fixedCommand(request, "/var/lib/runtime/vms/abc234de")
	if err != nil {
		t.Fatal(err)
	}
	disks := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "path=/proc/self/fd/") || !strings.Contains(arg, "image_type=raw") {
			continue
		}
		disks++
		if !strings.Contains(arg, ",_disable_io_uring=on,") || !strings.Contains(arg, ",_disable_aio=on,") {
			t.Fatalf("a fixed raw disk permits asynchronous fallback: %q", arg)
		}
	}
	if disks != 4 {
		t.Fatalf("the fixed creator has %d raw disks, want four", disks)
	}
}
