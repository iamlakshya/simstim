//go:build linux

package host

import "testing"

func TestOrdinaryStoppedCleanupRequiresActualNativeOwner(t *testing.T) {
	var purge *purgeOwnerV1
	if err := purge.destroyOrdinarySessionResourcesV1(nil, nil); err == nil {
		t.Fatal("ordinary storage cleanup accepted a missing original native source")
	}
	if err := purge.finishOrdinarySessionMetadataV1(nil, nil); err == nil {
		t.Fatal("ordinary terminal cleanup accepted a missing original native source")
	}
	if err := purge.removeOrdinarySessionV1(nil, nil); err == nil {
		t.Fatal("ordinary removal accepted a missing original operation")
	}
	if err := purge.removeOrdinaryWorktreeV1(nil, nil, nil); err == nil {
		t.Fatal("worktree removal accepted descriptions without the original native stop")
	}
	var worktree *ordinaryWorktreeCleanupV1
	if err := worktree.check(); err == nil {
		t.Fatal("worktree file cleanup accepted a missing original native owner")
	}
}

func TestOrdinaryStoppedStorageExcludesAllSandboxResources(t *testing.T) {
	for _, name := range []string{"vm", "chv.json", "firecracker.json", "gvisor", "gvisor.lock", "process.json", "process-root", "home", "work", "state", "members", "seed.bundle", "seed-includes", "../another", "unrelated"} {
		if knownStoppedOrdinaryResourceV1(name) {
			t.Fatal("the bounded ordinary source received sandbox or unrelated storage rights", name)
		}
	}
}
