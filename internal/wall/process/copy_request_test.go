package process

import (
	"strings"
	"testing"
)

func TestCopyFactoryDescriptorRoleIsVersioned(t *testing.T) {
	for _, test := range []struct {
		version, fd int
		valid       bool
	}{{1, 6, true}, {2, 6, false}, {2, 7, true}, {3, 7, false}} {
		request := Request{V: test.version, Mounts: []MountRequest{{FD: test.fd, Guest: "/work", Write: true}}}
		if err := request.Validate(); (err == nil) != test.valid {
			t.Errorf("version%d fd%d: %v", test.version, test.fd, err)
		}
	}
	legacy := `{"v":1,"probe":false,"dns":false,"mounts":[{"fd":6,"guest":"/work","write":true}]}`
	if _, err := DecodeRequest(strings.NewReader(legacy)); err != nil {
		t.Fatalf("V1 compatibility: %v", err)
	}
	private := strings.Replace(legacy, `"v":1`, `"v":2`, 1)
	if _, err := DecodeRequest(strings.NewReader(private)); err == nil {
		t.Fatal("V2 mount claimed the original control descriptor")
	}
	for _, name := range []string{"/.copy-staging", "/.copy-staging/data"} {
		if err := (Request{V: 2, Mounts: []MountRequest{{FD: 7, Guest: name, Write: true}}}).Validate(); err == nil {
			t.Fatal("caller replaced private fresh-root staging")
		}
	}
}
