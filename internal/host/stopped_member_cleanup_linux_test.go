//go:build linux

package host

import (
	"context"
	"testing"
)

func TestStoppedMemberCleanupRequiresActualWholePrimary(t *testing.T) {
	for _, owner := range []*stoppedCleanupOwnerV1{nil, {}, {id: "parent234"}} {
		if err := owner.removeMemberRoots(context.Background()); err == nil {
			t.Fatal("member metadata or a missing native owner authorised group cleanup")
		}
	}
}

func TestStoppedMemberStorageCannotNominatePrimaryRuntime(t *testing.T) {
	for _, name := range []string{"vm", "firecracker.json", "chv.json", "gvisor", "process-root", "home", "work", "../other", "unrelated", "op.lock"} {
		if knownStoppedMemberResourceV1(name) {
			t.Fatal("member storage received primary or unrelated cleanup rights", name)
		}
	}
	for _, name := range []string{"paste", "pull", "debug", "inputs", "rename.unsaved"} {
		if !knownStoppedMemberResourceV1(name) {
			t.Fatal("member storage lost a supported fixed resource role", name)
		}
	}
}
