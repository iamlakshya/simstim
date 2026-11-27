package jail

import (
	"context"
	"testing"
)

func TestNativeLiveCannotBeMintedFromZeroOrCopiedLifecycleData(t *testing.T) {
	cases := []*Cmd{nil, {}, {phase: phaseRunning, pid: 1234, pidfd: -1}, {phase: phaseUncertain, owned: true, pid: 1234, pidfd: 0}, {phase: phaseWaiting, owned: true, physical: true, pid: 1234, pidfd: 0}}
	for _, command := range cases {
		if err := command.CheckNativeLiveV1(context.Background()); err == nil {
			t.Fatal("copied lifecycle data became an original native owner")
		}
	}
}
