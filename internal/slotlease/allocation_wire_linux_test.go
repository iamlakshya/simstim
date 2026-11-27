//go:build linux

package slotlease

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRootAllocationParentWireUsesActualStrictDecoder(t *testing.T) {
	raw, err := os.ReadFile("testdata/root-allocation-parent-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var record allocation
	if err := decodeRecord(bytes.NewReader(raw), &record); err != nil {
		t.Fatal("the root producer's exact parent-bearing wire was rejected:", err)
	}
	if record.Cgroup == nil || record.Cgroup.ParentDev != 1 || record.Cgroup.ParentIno != 3 || record.SlotGID != 23000 {
		t.Fatal("strict allocation decoder lost original parent or slot group fields")
	}
	unknown := strings.Replace(string(raw), `"parent_ino":3`, `"parent_ino":3,"parent_future":true`, 1)
	if err := decodeRecord(strings.NewReader(unknown), new(allocation)); err == nil {
		t.Fatal("unknown cgroup fields weakened the strict native allocation codec")
	}
	if err := decodeRecord(strings.NewReader(string(raw)+strings.Repeat(" ", 65537)+"{}"), new(allocation)); err == nil {
		t.Fatal("over-bound trailing input became an allocation")
	}
	if err := checkCgroupParent(nil, cgroupIdentity{}); err == nil {
		t.Fatal("missing original parent data became native capture")
	}
	ordinary, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	if err := checkCgroupParent(ordinary, cgroupIdentity{Dev: 1, ParentDev: 1, ParentIno: 3}); err == nil {
		t.Fatal("ordinary directory or forged native parent became a cgroup capability")
	}
	if err := decodeRecord(bytes.NewReader(append(raw, []byte(`{}`)...)), new(allocation)); err == nil {
		t.Fatal("a second wire object bypassed the strict native allocation decoder")
	}
}
