//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestGuestBootCertificationRequiresBothActualV2ResponsesAndOwnedLiveStart(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{Arch: spec.VM.Image.Arch, Kernel: "6.18.63", Subuid: true, KVM: api.KVM{InGroupManager: true}}
	for _, test := range []string{"hello-only", "v1-prepare", "v1-terminal", "not-started", "forced", "ended", "unproven-exit", "exit-error", "exit-signal", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := newSandbox(driver, spec, nil, "subuid", ctx)
			s.started = true
			s.helloChecked = true
			s.bootFacts = facts
			s.command = &jail.Cmd{}
			s.control = &runnerControl{}
			prepared, terminal := proto.GuestOwnedPTYV2, proto.GuestOwnedPTYV2
			switch test {
			case "hello-only":
				prepared = ""
				terminal = ""
			case "v1-prepare":
				prepared = proto.GuestPTY1000V1
			case "v1-terminal":
				terminal = proto.GuestPTY1000V1
			case "not-started":
				s.started = false
			case "forced":
				s.forced = true
			case "ended":
				s.physicalExit = true
				close(s.done)
			case "unproven-exit":
				s.exit = wall.Exit{Code: 0}
				close(s.done)
			case "exit-error":
				s.physicalExit = true
				s.exit = wall.Exit{Code: 0, Err: errors.New("owned cleanup failed")}
				close(s.done)
			case "exit-signal":
				s.physicalExit = true
				s.exit = wall.Exit{Code: 0, Signal: "killed"}
				close(s.done)
			case "cancelled":
				cancel()
			}
			if s.CertifyGuestBootV1(ctx, prepared, terminal) == nil {
				t.Fatal("ordinary/stale/dead evidence promoted readiness")
			}
			if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
				t.Fatal("refused certification wrote a proof")
			}
		})
	}
}

func TestOldHelloOnlyBootProofIsNeverTreatedAsPreparedV2(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{Arch: spec.VM.Image.Arch, Kernel: "6.18.63"}
	key, err := evidenceKey(spec.VM.Runtime, facts, "test-boot-helper")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(driver.options.Evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(bootEvidence{Version: 1, Key: key, Booted: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(driver.options.Evidence, key+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if driver.checkBootEvidence(spec.VM.Runtime, facts) == nil {
		t.Fatal("legacy Hello-only proof retained driver readiness")
	}
}

// This tests the owned predicate/metadata seam only. Native tests must establish
// the actual process handles, responses and exit represented by these fields.
func TestQuickOwnedNaturalExitZeroCanCertifyCompatibilityAfterBothV2Responses(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{Arch: spec.VM.Image.Arch, Kernel: "6.18.63", Subuid: true, KVM: api.KVM{InGroupManager: true}}
	sandbox := newSandbox(driver, spec, nil, "subuid", context.Background())
	sandbox.started = true
	sandbox.helloChecked = true
	sandbox.bootFacts = facts
	sandbox.command = &fakeVMProcess{physical: true}
	sandbox.control = &runnerControl{}
	sandbox.exit = wall.Exit{Code: 0}
	sandbox.physicalExit = true // Model the original owner's completed native wait.
	close(sandbox.done)
	if err := sandbox.CertifyGuestBootV1(context.Background(), proto.GuestOwnedPTYV2, proto.GuestOwnedPTYV2); err != nil {
		t.Fatal("short successful owned boot was mislabelled unavailable", err)
	}
	if err := driver.checkBootEvidence(spec.VM.Runtime, facts); err != nil {
		t.Fatal("actual v2 compatibility seam did not publish schema2", err)
	}
}
