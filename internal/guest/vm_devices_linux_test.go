//go:build linux

package guest

import (
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMicroVMEntryCannotBeEnabledByEnvironmentOrMappedRoot(t *testing.T) {
	for _, test := range []struct {
		pid, uid int
		fs       int64
	}{{2, 0, unix.EROFS_SUPER_MAGIC_V1}, {1, 1000, unix.EROFS_SUPER_MAGIC_V1}, {1, 0, unix.EXT4_SUPER_MAGIC}} {
		if vmEntryGuard(test.pid, test.uid, test.fs) == nil {
			t.Fatal("unverified ordinary guest selected privileged VM init")
		}
	}
	if err := vmEntryGuard(1, 0, unix.EROFS_SUPER_MAGIC_V1); err != nil {
		t.Fatal(err)
	}
}

func TestVMDiskSerialAndReadonlyRolesFailBeforeMount(t *testing.T) {
	data := map[string]string{}
	for name, serial := range map[string]string{"vda": "tools", "vdb": "image", "vdc": "agents", "vdd": "session"} {
		data["/sys/block/"+name+"/serial"] = serial
		data["/sys/block/"+name+"/ro"] = "1"
	}
	data["/sys/block/vdd/ro"] = "0"
	read := func(path string) (string, error) {
		v, ok := data[path]
		if !ok {
			return "", fmt.Errorf("missing")
		}
		return v, nil
	}
	if err := verifyVMDisks(read); err != nil {
		t.Fatal(err)
	}
	data["/sys/block/vdc/serial"] = "session"
	if verifyVMDisks(read) == nil {
		t.Fatal("wrong disk order accepted")
	}
	data["/sys/block/vdc/serial"] = "agents"
	data["/sys/block/vdb/ro"] = "0"
	if verifyVMDisks(read) == nil {
		t.Fatal("writable immutable image accepted")
	}
}
