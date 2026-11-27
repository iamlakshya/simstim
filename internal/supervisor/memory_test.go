package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The memory facts of a walled session are the supervisor's, from the
// unit's own files: the warning near the cap, and a kill at the cap. A
// claim of the guest cannot hide the warning.
func TestMemoryFactsAreTheSupervisors(t *testing.T) {
	cg := t.TempDir()
	set := func(name, v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cg, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const gib = 1 << 30
	set("memory.max", strconv.Itoa(gib))
	set("memory.current", strconv.Itoa(100<<20))
	set("memory.events", "max 0\noom 0\noom_kill 0")
	set("memory.stat", "anon 0\nfile 0\nshmem 0\nslab_reclaimable 0")
	old := unitDir
	unitDir = func(string) string { return cg }
	t.Cleanup(func() { unitDir = old })
	id, g, _ := supervised(t)

	status := func(want func(host.Status) bool, what string) host.Status {
		t.Helper()
		var st host.Status
		waitFor(t, what, func() bool {
			m, err := host.LoadManifest(id)
			if err == nil {
				st = m.Status
			}
			return err == nil && want(st)
		})
		return st
	}
	// 96 % of the cap that the kernel cannot reclaim: the warning.
	set("memory.current", strconv.Itoa(gib/100*96))
	st := status(func(s host.Status) bool { return s.Source == host.SrcMemory }, "the memory warning")
	if st.State != host.StateNeedsInput || st.Claimed || st.Reason != host.MemoryReason("1 of 1 GiB") {
		t.Errorf("the warning = %+v", st)
	}
	g.send(proto.CtlEvent{Event: proto.EventStatus, Session: id, State: host.StateWorking, Source: "output"})
	waitFor(t, "the claim", func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && m.Guest != nil && m.Guest.Source == "output"
	})
	if m, _ := host.LoadManifest(id); m.Status.Source != host.SrcMemory {
		t.Errorf("a claim hid the warning: %+v", m.Status)
	}
	// Below 80 %, the episode ends, and the claim shows.
	set("memory.current", strconv.Itoa(100<<20))
	st = status(func(s host.Status) bool { return s.Source == "output" }, "the end of the episode")
	if st.State != host.StateWorking || !st.Claimed {
		t.Errorf("after the episode = %+v", st)
	}
	// A kill at the cap is the supervisor's own fact.
	set("memory.events", "max 3\noom 1\noom_kill 1")
	st = status(func(s host.Status) bool { return s.OOM }, "the kill")
	if st.Reason != host.OOMChildReason {
		t.Errorf("after the kill = %+v", st)
	}
}
