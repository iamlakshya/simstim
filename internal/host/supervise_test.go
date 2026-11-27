package host

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func unitWatchFiles(t *testing.T, dir string, current, cache, hits, kills int64) {
	t.Helper()
	for name, body := range map[string]string{
		"memory.max":     "1000\n",
		"memory.current": fmt.Sprintf("%d\n", current),
		"memory.stat":    fmt.Sprintf("file %d\nshmem 0\nslab_reclaimable 0\n", cache),
		"memory.events":  fmt.Sprintf("max %d\noom_kill %d\n", hits, kills),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnitWatchAccountsForEachKillAcrossTickAndEnd(t *testing.T) {
	dir := t.TempDir()
	unitWatchFiles(t, dir, 500, 0, 0, 3)
	w := NewUnitWatch(dir)
	if w.Base() != 3 || w.End() != 0 {
		t.Fatal("the baseline counted an earlier run's kills")
	}
	unitWatchFiles(t, dir, 500, 0, 1, 4)
	if tick := w.Tick(); tick.Kills != 0 {
		t.Fatalf("a tick claimed a kill before the agent's end could take it: %+v", tick)
	}
	if kills := w.End(); kills != 1 {
		t.Fatalf("the agent's end lost the noted kill: %d", kills)
	}
	if tick := w.Tick(); tick.Kills != 0 {
		t.Fatalf("a tick counted the agent's kill again: %+v", tick)
	}
	unitWatchFiles(t, dir, 500, 0, 2, 5)
	if tick := w.Tick(); tick.Kills != 0 {
		t.Fatalf("a new child kill skipped its observation tick: %+v", tick)
	}
	if tick := w.Tick(); tick.Kills != 1 {
		t.Fatalf("the next tick lost the child's kill: %+v", tick)
	}
	if w.End() != 0 || w.Tick().Kills != 0 || w.Base() != 3 {
		t.Fatal("an accounted kill was counted again or changed the baseline")
	}
}

func TestUnitWatchUsesPinnedMemoryAndWarningEpisodes(t *testing.T) {
	dir := t.TempDir()
	unitWatchFiles(t, dir, 500, 0, 0, 0)
	w := NewUnitWatch(dir)
	unitWatchFiles(t, dir, 950, 800, 1, 0)
	if tick := w.Tick(); tick.Warn || tick.Usage != "" {
		t.Fatalf("reclaimable cache caused a memory warning: %+v", tick)
	}
	unitWatchFiles(t, dir, 900, 0, 2, 0)
	if tick := w.Tick(); !tick.Warn || tick.Usage == "" || tick.Calm {
		t.Fatalf("pinned memory under pressure did not begin a warning: %+v", tick)
	}
	if tick := w.Tick(); tick.Warn || tick.Usage == "" {
		t.Fatalf("one episode raised a second warning or lost its usage: %+v", tick)
	}
	unitWatchFiles(t, dir, 799, 0, 2, 0)
	if tick := w.Tick(); !tick.Calm || tick.Warn || tick.Usage != "" {
		t.Fatalf("the warning did not clear below the existing low threshold: %+v", tick)
	}
	unitWatchFiles(t, dir, 950, 0, 2, 0)
	if tick := w.Tick(); !tick.Warn {
		t.Fatalf("the existing high threshold did not start a new episode: %+v", tick)
	}
}

func TestUnitWatchWithoutCgroupObservesNothing(t *testing.T) {
	for _, watch := range []*UnitWatch{nil, NewUnitWatch("")} {
		if watch.Base() != 0 || watch.End() != 0 || watch.Tick() != (MemTick{}) {
			t.Fatal("a missing unit supplied memory evidence")
		}
	}
}

func TestSupervisorPeerFacadeKeepsKernelPeerChecks(t *testing.T) {
	const id = "abcd2345"
	for _, tc := range []struct {
		name   string
		cgroup string
		alive  bool
		want   bool
	}{
		{"outside", outsideCgroup(id), true, true},
		{"own session", ownCgroup(id), true, false},
		{"other session", otherCgroup(id), true, false},
		{"missing cgroup", "", true, false},
		{"exited peer", outsideCgroup(id), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := fakePeer(t, tc.cgroup, func() bool { return tc.alive })
			if got := FullPeer(nil, id, true); got != tc.want {
				t.Fatalf("full peer = %v, want %v", got, tc.want)
			}
			if closed.Load() != 1 {
				t.Fatal("the peer check did not close its borrowed pidfd")
			}
		})
	}
}

func TestSupervisorLogFacadeRotatesAndKeepsPrivateMode(t *testing.T) {
	name := filepath.Join(t.TempDir(), "guest-holder.log")
	log, err := OpenLog(name)
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte("x"), holderLogMax)
	if _, err := log.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want []byte
	}{{name + ".1", first}, {name, []byte("next\n")}} {
		got, err := os.ReadFile(tc.path)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Fatalf("the log lost bytes across rotation: %s: %v", filepath.Base(tc.path), err)
		}
		st, err := os.Stat(tc.path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("the log is not private: %s: %v", filepath.Base(tc.path), err)
		}
	}
}
