package host

import (
	"context"
	"testing"
)

func TestStoppedPurgeCleanupRequiresActualNativeOwner(t *testing.T) {
	if err := DestroyStoppedSessionResourcesV1(context.Background(), nil, nil); err == nil {
		t.Fatal("a missing physical stop source authorised native cleanup")
	}
}

func TestFootprintRolesRejectArbitraryOutsidePaths(t *testing.T) {
	for _, test := range []struct{ role, leaf string }{{"systemd_user", "../../other"}, {"default_wants", "unrelated.service"}, {"terminfo:../x", "xterm"}, {"terminfo:x", "../other"}, {"slice_control", "another.conf"}, {"parent:slice_dropin", "another.slice.d"}} {
		if knownFootprintLeafV1(test.role, test.leaf) {
			t.Fatal("a descriptor role accepted another outside resource", test)
		}
	}
	for _, bucket := range []string{"x", "78", "a", "41"} {
		if !validTerminfoBucketV1(bucket) {
			t.Fatal("a compiler bucket was refused", bucket)
		}
	}
}
