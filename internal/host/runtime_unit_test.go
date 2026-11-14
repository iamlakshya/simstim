package host_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// A holder whose cgroup does not show its unit, as here, learns it from
// the variable that launchHolder gives it, and records it before its first
// save (docs/contributing/cli-spec.md 9.5): when its agent starts, in the
// save after the agent ends, and when its agent cannot start at all. The
// real holder runs each time, started by a resume, whose session's ID the
// test knows before the start.
func TestTheHolderRecordsItsUnitBeforeItsFirstSave(t *testing.T) {
	h := hosttest.New(t)
	agent := filepath.Join(h.Home, "agent.sh")
	writeFile(t, agent, "#!/bin/sh\nexit 3\n")
	if err := os.Chmod(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	s := start(t, "a", agent)
	waitState(t, s.ID, host.StateErrored)
	unit := host.UnitName(s.ID)
	t.Setenv(host.UnitEnv, unit)

	// The agent starts, and ends: the holder saves at its start and at the
	// end, and both saves keep the unit.
	resume(t, api.ResumeArgs{ID: s.ID})
	waitState(t, s.ID, host.StateErrored)
	if m := manifest(t, s.ID); m.Run.Unit != unit || m.Run.AgentPID == 0 {
		t.Errorf("after a start and an end: run %+v", m.Run)
	}

	// Keep the manager fixture alive for the recorded synthetic unit. The
	// direct launch above has already checked UnitEnv without that fixture.
	units := fakeSystemd(t, h, true)

	// The agent's program is gone, so the agent cannot start: the save of
	// the failed start holds the unit too.
	if err := os.Remove(agent); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Resume(api.ResumeArgs{ID: s.ID}); err == nil || !strings.Contains(err.Error(), "agent.sh") {
		t.Fatalf("the resume did not report its missing agent: %v", err)
	}
	if m := manifest(t, s.ID); m.Run.Unit != unit || m.Status.State != host.StateErrored {
		t.Errorf("after a start that failed: run %+v, status %+v", m.Run, m.Status)
	}
	// This fixture has no remaining service once its failed holder exits.
	// Retire the fake manager record rather than signal an old numeric PID.
	if err := os.Remove(filepath.Join(units, unit)); err != nil {
		t.Fatal(err)
	}
}
