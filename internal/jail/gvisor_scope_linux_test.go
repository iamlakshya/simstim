package jail

import (
	"encoding/json"
	"testing"
)

func TestGvisorParentFilesystemLayerRequiresActualAppliedScopeProof(t *testing.T) {
	c := &setup{GvisorV1Version: 1, Userns: true, Net: NetNone, Seccomp: profileGvisorV1, gvisorOuterNative: true}
	if _, err := newGvisorParentFilesystemRulesV1(c, 6); err == nil {
		t.Fatal("a caller marker omitted a new scope without actual common enforcement")
	}
	fake := &gvisorCommonScopeV1{owner: c, abi: 6}
	c.gvisorCommonScope = fake
	if _, err := newGvisorParentFilesystemRulesV1(c, 6); err == nil {
		t.Fatal("copied scope data became native enforcement proof")
	}
	// Private success capabilities are absent from every serialised setup.
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var decoded setup
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.gvisorCommonScope != nil || decoded.gvisorOuterNative || decoded.gvisorOuterGate != nil {
		t.Fatal("serialised data carried original native scope authority")
	}
	if _, err := installGvisorCommonScopeV1(&decoded, 6); err == nil {
		t.Fatal("a decoded setup installed a common scope without original A ownership")
	}
}

func TestGvisorScopeProofCannotBeReusedAfterClose(t *testing.T) {
	original := &setup{GvisorV1Version: 1, gvisorOuterNative: true}
	proof := &gvisorCommonScopeV1{owner: original, abi: 6}
	proof.applied = &gvisorScopeAppliedV1{original: proof}
	copied := *proof
	if copied.applied.original == &copied {
		t.Fatal("a copied proof retained the original success identity")
	}
	if err := proof.Close(); err != nil {
		t.Fatal(err)
	}
	if proof.applied != nil || proof.owner != nil || proof.task != nil || proof.taskRef != nil {
		t.Fatal("a closed scope proof retained an owned native role")
	}
	if err := proof.checkOriginal(original, 6); err == nil {
		t.Fatal("a closed success proof selected a weaker filesystem layer")
	}
}
