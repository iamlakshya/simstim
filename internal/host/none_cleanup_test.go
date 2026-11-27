package host

import (
	"context"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func noneCleanFixtureV1() Manifest {
	at := time.Unix(100, 0).UTC()
	boot := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	return Manifest{ID: "a2a2a2a2", Wall: &WallRecord{Level: "none", Driver: "none"}, Run: Runtime{BootID: boot, Generation: "original-run", HolderPID: 1001}, Life: &Life{Phase: LifeStopped, End: &LifeEnd{Kind: EndStop, At: at}}, NoneCleanup: &NoneCleanupV1{Version: 1, Scope: "n5", Boot: boot, Generation: "original-run", SupervisorPID: 1001, InitPID: 1002, SupervisorBirth: "42", InitBirth: "43", At: at.Add(time.Second), Directory: cachefs.Reference{Version: 1, Identity: "fixture-directory-data", Mode: 0700, Modified: at, Directory: true}, Holder: cachefs.Reference{Version: 1, Identity: "fixture-holder-data", Mode: 0600, Modified: at}}}
}

func TestNoneCleanupDataRequiresTheExactOriginalRunAndScope(t *testing.T) {
	base := noneCleanFixtureV1()
	if !validNoneCleanupV1(&base, base.Run.BootID) {
		t.Fatal("the original exact cleanup data was rejected")
	}
	for name, change := range map[string]func(*Manifest){
		"guest-claim":           func(m *Manifest) { m.NoneCleanup = nil; m.Guest = &GuestClaims{} },
		"legacy":                func(m *Manifest) { m.Wall = nil },
		"process":               func(m *Manifest) { m.Wall = &WallRecord{Level: "process", Driver: "process"} },
		"member":                func(m *Manifest) { m.SandboxOf = "b2b2b2b2" },
		"running":               func(m *Manifest) { m.Life.Phase = LifeRunning },
		"sandbox-end":           func(m *Manifest) { m.Life.End.Kind = EndSandbox },
		"missing-end":           func(m *Manifest) { m.Life.End = nil },
		"stale-generation":      func(m *Manifest) { m.Run.Generation = "new-run" },
		"stale-boot":            func(m *Manifest) { m.Run.BootID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" },
		"wrong-pid":             func(m *Manifest) { m.Run.HolderPID++ },
		"same-actor":            func(m *Manifest) { m.NoneCleanup.InitPID = m.NoneCleanup.SupervisorPID },
		"whole-unit-scope":      func(m *Manifest) { m.NoneCleanup.Scope = "unit" },
		"wrong-version":         func(m *Manifest) { m.NoneCleanup.Version = 2 },
		"missing-birth":         func(m *Manifest) { m.NoneCleanup.InitBirth = "" },
		"bad-birth":             func(m *Manifest) { m.NoneCleanup.SupervisorBirth = "0042" },
		"missing-root-identity": func(m *Manifest) { m.NoneCleanup.Directory = cachefs.Reference{} },
		"wrong-holder-mode":     func(m *Manifest) { m.NoneCleanup.Holder.Mode = 0644 },
		"premature":             func(m *Manifest) { m.NoneCleanup.At = m.Life.End.At.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			m := CloneManifest(base)
			change(&m)
			if validNoneCleanupV1(&m, base.Run.BootID) {
				t.Fatal("foreign or uncertain cleanup data was accepted")
			}
		})
	}
	copy := CloneManifest(base)
	copy.NoneCleanup.Generation = "changed"
	if base.NoneCleanup.Generation != "original-run" {
		t.Fatal("the clone aliases cleanup data")
	}
}

func TestNoneCleanupDataCannotIssueANativeContinuation(t *testing.T) {
	var absent *NoneColdResumeV1
	if err := absent.Check(context.Background(), &SessionOperation{}); err == nil {
		t.Fatal("a missing source issued a continuation")
	}
	if _, err := captureNoneColdResumeV1(context.Background(), &bridgeResumePreparationV1{}); err == nil {
		t.Fatal("a copied request DTO issued a None source")
	}
}
