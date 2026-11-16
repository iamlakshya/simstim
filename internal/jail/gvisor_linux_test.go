package jail

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestGvisorNestedNamespaceKeepsOriginalPIDOne(t *testing.T) {
	attr := gvisorExecNamespacesV1()
	if attr.Cloneflags&unix.CLONE_NEWUSER == 0 || attr.Cloneflags&unix.CLONE_NEWNS == 0 || attr.Cloneflags&unix.CLONE_NEWNET == 0 || attr.Cloneflags&unix.CLONE_NEWPID != 0 || attr.Pdeathsig != unix.SIGKILL {
		t.Fatal("gvisor lost its ancestor PID 1 death boundary", attr)
	}
	if len(attr.UidMappings) != 1 || len(attr.GidMappings) != 1 {
		t.Fatal("gvisor expanded the original identity map")
	}
	for _, mapping := range append(attr.UidMappings, attr.GidMappings...) {
		if mapping.ContainerID != 0 || mapping.HostID != 0 || mapping.Size != 1 {
			t.Fatal("gvisor mapped another parent identity", mapping)
		}
	}
}

func TestGvisorPurposeCannotBeCarriedByAnOldOrRawBindStage(t *testing.T) {
	set := &setup{GvisorV1Version: 1, BindsV1Version: 1, Userns: true, Seccomp: profileGvisorV1, Binds: []bind{{SuppliedV1: true}}}
	if got, err := checkFileRulesStageV1(stageInitGvisorV1, set); err != nil || got != stageInit {
		t.Fatalf("the actual versioned purpose failed: %q %v", got, err)
	}
	for _, stage := range []string{stageInit, stageInitBindsV1, stageInitFilesV1, stageExec, stageOuter} {
		if _, err := checkFileRulesStageV1(stage, set); err == nil {
			t.Fatal("an older stage adopted the bootstrap profile", stage)
		}
	}
	set.Binds[0].SuppliedV1 = false
	if _, err := checkFileRulesStageV1(stageExecGvisorV1, set); err == nil {
		t.Fatal("the gvisor stage reopened a raw path as authority")
	}
}

func TestGvisorNativeMapRejectsExtraAndMalformedIdentities(t *testing.T) {
	if !singleGvisorMapV1("         0          0          1\n") {
		t.Fatal("the kernel's exact single map was refused")
	}
	for _, text := range []string{"0 0 1", "0 0 1\n1 1 1\n", "0 1000 1\n", "00 0 1\n", "0 0 +1\n", "0 0 4294967295\n", "0\u00a00 1\n", "0 0 1\ntrailing"} {
		if singleGvisorMapV1(text) {
			t.Fatal("an unproved mapping became the single-owner namespace", text)
		}
	}
}
