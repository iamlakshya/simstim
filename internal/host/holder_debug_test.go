package host

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// holder.log turns over to holder.log.1 at its limit, so the two files
// keep at most twice the limit.
func TestTheHolderLogTurnsOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holder.log")
	l, err := openHolderLog(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	log := newHolderLog(l, nil)
	for i := range 20 {
		log.Info("a line of the holder's log", "n", i)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil || st.Size() > 200 || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v; want 200 bytes at most, mode 600", p, st, err)
		}
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "n=19") {
		t.Errorf("the newest line is not in %s: %s", path, b)
	}
}

// lockedBuffer is a log that tests read while a holder writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// At a kill, the holder writes the unit's largest processes by PSS to
// holder.log, and with the memory category on, the largest by RSS to the
// debug file. It reads smaps_rollup of the largest oomRead by RSS only, and
// names each process by its comm, never its command line.
func TestTheSnapshotAtAnOOMKill(t *testing.T) {
	const id = "oomsnap1"
	pids := []string{"101", "102", "103"}
	proc := map[string]string{
		"101/comm":         "claude\n",
		"101/cmdline":      "claude\x00--secret-flag\x00",
		"101/statm":        "9000 512 100 10 0 400 0\n",
		"101/smaps_rollup": "00400000-7fff [rollup]\nRss:                2048 kB\nPss:                1024 kB\n",
		"102/comm":         "node\n",
		"102/statm":        "9000 2048 100 10 0 400 0\n",
		"102/smaps_rollup": "Rss: 8192 kB\nPss: 6144 kB\n",
		// 103 is gone by the time the holder reads it.
	}
	// Many small processes: only the largest by RSS are read in full.
	for i := range 30 {
		pid := strconv.Itoa(300 + i)
		pids = append(pids, pid)
		proc[pid+"/comm"] = "small\n"
		proc[pid+"/statm"] = "100 1 1 1 0 1 0\n"
		proc[pid+"/smaps_rollup"] = "Rss: 4 kB\nPss: 2 kB\n"
	}
	files := map[string]string{"cgroup.procs": strings.Join(append([]string{"400"}, pids...), "\n") + "\n"}
	for name, body := range memFiles {
		files[name] = body
	}
	dir := fakeUnitCgroup(t, id, files)
	unit := strings.TrimPrefix(dir, cgroupRoot)
	proc["self/cgroup"] = "0::" + unit + "\n"
	// cc1 holds the most memory when the warning starts, and the kill
	// stops it.
	proc["400/comm"] = "cc1\n"
	proc["400/statm"] = "90000 65536 100 10 0 400 0\n"
	proc["400/smaps_rollup"] = "Rss: 262144 kB\nPss: 262144 kB\n"
	fakeProc(t, proc)
	f, err := diag.Open(filepath.Join(t.TempDir(), "debug.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var log lockedBuffer
	h := &Holder{id: id, log: newHolderLog(&log, nil)}
	h.startMemory()
	h.dbg.tr.Store(&holderTrace{f: f})
	// The memory warning starts an episode: the holder notes the processes.
	h.mem.on, h.mem.mark = true, true
	h.snapshotBefore(false)
	hosttestWait(t, "the snapshot before the kill", func() bool { return h.dbg.before.Load() != nil })
	writeTestFile(t, filepath.Join(dir, "cgroup.procs"), strings.Join(pids, "\n")+"\n")
	h.snapshotOOM(h.mem.dir, procRoot, 3)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	text := log.String()
	before, left := strings.Index(text, "before the kill"), strings.Index(text, "left in the session's unit after the kill")
	if before < 0 || left < before || !strings.Contains(text[before:left], "comm=cc1") || strings.Contains(text[left:], "comm=cc1") {
		t.Fatalf("holder.log does not name the process killed before the kill only: %s", text)
	}
	after := text[left:]
	node, claude := strings.Index(after, "comm=node"), strings.Index(after, "comm=claude")
	if node < 0 || claude < node || !strings.Contains(after, "kills=3 procs=32") || !strings.Contains(after, "pss=6291456") ||
		strings.Count(after, "comm=") != oomTop {
		t.Fatalf("holder.log = %s", text)
	}
	recs := readTrace(t, f.Path())
	if len(recs) != 1 || recs[0].Ev != "oom" || recs[0].Cat != diag.CatMemory || recs[0].N != 32 || len(recs[0].Procs) != oomRead {
		t.Fatalf("records = %+v", recs)
	}
	r := recs[0]
	if p := r.Procs[1]; p.PID != 101 || p.Comm != "claude" || p.RSS != 2048<<10 || p.PSS != 1024<<10 {
		t.Errorf("claude = %+v", p)
	}
	if len(r.Before) == 0 || r.Before[0].Comm != "cc1" || r.BeforeT == 0 || r.BeforeT > r.T {
		t.Errorf("before the kill = %+v at %d", r.Before, r.BeforeT)
	}
	// The unit's memory, as a sample reads it.
	if r.Mem == nil || *r.Mem != wantMem {
		t.Errorf("the record's memory = %+v, want %+v", r.Mem, wantMem)
	}
	raw, _ := os.ReadFile(f.Path())
	if strings.Contains(text, "secret-flag") || bytes.Contains(raw, []byte("secret-flag")) {
		t.Error("a command line reached the files")
	}
}

// Once the holder exits, as for a resume, a debug start is refused: its
// trace would get no stop record.
func TestNoDebugStartsAsTheHolderExits(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := &Holder{id: "exiting1", m: &Manifest{}, log: slog.New(slog.DiscardHandler)}
	h.endAllDebug()
	_, perr := h.startDebug(api.DebugStartArgs{})
	if perr == nil || perr.Code != "exiting" || perr.Hint == "" || h.dbg.tr.Load() != nil {
		t.Fatalf("a start as the holder exits = %+v", perr)
	}
	h.traceAttach(&hclient{}, "a.1", nil)
	if h.dbg.tr.Load() != nil {
		t.Fatal("a traced attach opened a trace as the holder exits")
	}
}

// hosttestWait polls cond until it holds, for 10 s at most.
func hosttestWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// With debug off, or without the memory category, the warning takes no
// snapshot: it costs what it did.
func TestNoSnapshotBeforeWithoutTheMemoryCategory(t *testing.T) {
	h := &Holder{mem: memWatch{dir: t.TempDir(), on: true, mark: true}}
	h.snapshotBefore(false)
	f, err := diag.Open(filepath.Join(t.TempDir(), "debug.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SetCats(diag.CatsOf([]string{diag.CatAttach}))
	h.dbg.tr.Store(&holderTrace{f: f})
	h.snapshotBefore(false) // it starts no goroutine without the category
	if h.dbg.before.Load() != nil {
		t.Fatal("a snapshot was taken without the memory category")
	}
}

// Each kill gets one snapshot, whichever of the tick and the reap sees it
// first.
func TestEachKillGetsOneSnapshot(t *testing.T) {
	h := &Holder{}
	if h.newSnapshot(0) != 0 || h.newSnapshot(1) != 1 || h.newSnapshot(1) != 0 || h.newSnapshot(3) != 3 {
		t.Fatal("a kill count was snapshotted twice, or not at all")
	}
}

// A bridge that exits when idle skips its defers: it ends its debug first,
// so its file gets its stop record.
func TestAnIdleExitStopsTheBridgesDebug(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lines := diag.NewLines()
	b := &bridge{log: slog.New(lines), dbg: bridgeDebug{lines: lines}}
	b.dbg.mu.Lock()
	run, err := b.startDebug(diag.CatsOf([]string{diag.CatNet}))
	b.dbg.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	b.dbg.run.Store(run)
	liveBridges.Store(b, true)
	defer liveBridges.Delete(b)
	stopBridgesDebug("idle")
	if b.dbg.run.Load() != nil {
		t.Fatal("the bridge's debug is still on")
	}
	recs := readTrace(t, run.File.Path())
	if last := recs[len(recs)-1]; last.Ev != diag.EvStop || last.Reason != "idle" {
		t.Fatalf("the last record = %+v, want the stop", last)
	}
}

// A holder outside its own unit takes no snapshot.
func TestNoSnapshotOutsideTheUnit(t *testing.T) {
	fakeProc(t, map[string]string{"self/cgroup": "0::/system.slice/" + brand.Name + "-other.service\n"})
	var log lockedBuffer
	h := &Holder{id: "oomsnap2", log: newHolderLog(&log, nil)}
	h.startMemory()
	h.goSnapshot(1)
	if h.mem.dir != "" || log.String() != "" {
		t.Fatalf("outside its unit: folder %q, holder.log %s", h.mem.dir, log.String())
	}
}
