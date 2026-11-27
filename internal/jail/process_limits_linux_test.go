package jail

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestLandlockCannotChangeAnOutsideChildsResourceLimits(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	control := startTarget(t)
	if err := unix.Prlimit(control, unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 0, Max: 0}, nil); err != nil {
		t.Fatalf("the unjailed same-user control could not change its owned child's limit: %v", err)
	}
	target := startTarget(t)
	var before unix.Rlimit
	if err := unix.Prlimit(target, unix.RLIMIT_NOFILE, nil, &before); err != nil {
		t.Fatal(err)
	}
	out := jailed(t, Spec{Seccomp: ProfileConverter}, probeIn{Limits: target})
	if out.Limits == "" || out.Limits == "ok" {
		t.Errorf("the jail changed an outside process's resource limit: %q", out.Limits)
	}
	var after unix.Rlimit
	if err := unix.Prlimit(target, unix.RLIMIT_NOFILE, nil, &after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the outside child's limit changed: before=%+v after=%+v", before, after)
	}
}
