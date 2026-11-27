//go:build linux

package guest

import (
	"reflect"
	"testing"
)

func TestDockerCleanupUsesCanonicalMountParserAndOnlyPrivateDescendants(t *testing.T) {
	snapshot := `1 0 0:1 / / rw - overlay overlay rw
2 1 0:2 / /.persist/docker/overlay2/a/merged rw - overlay overlay rw
3 2 0:3 / /.persist/docker/overlay2/a/merged/proc rw - proc proc rw
4 1 0:4 / /.persist/docker-other/foreign rw - tmpfs tmpfs rw
5 1 0:5 / /work rw - ext4 /dev/vdd rw
6 1 0:6 / /.persist/docker/overlay2/a/merged rw - overlay overlay rw
`
	targets, err := vmDockerMountTargets(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/.persist/docker/overlay2/a/merged/proc", "/.persist/docker/overlay2/a/merged", "/.persist/docker/overlay2/a/merged"}
	if !reflect.DeepEqual(targets, want) {
		t.Fatal("cleanup missed stacked child mounts or selected foreign data", targets)
	}
	for _, invalid := range []string{"", `1 0 0:1 / /.persist/docker/../foreign rw - tmpfs tmpfs rw`, `1 0 0:1 / /.persist/docker/bad\000name rw - tmpfs tmpfs rw`} {
		if _, err := vmDockerMountTargets(invalid); err == nil {
			t.Fatal("bad topology decoded for privileged cleanup")
		}
	}
}
