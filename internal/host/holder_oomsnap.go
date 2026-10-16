package host

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
)

// At an out-of-memory kill, the holder reads the processes in the session's
// unit, to show what held memory there (#40, decision 0025). It names a
// process by /proc/<pid>/comm only, never by its command line. The snapshot
// runs with debug on or off, once for each kill, in a goroutine of its own,
// so it holds up neither the tick nor the end of the session.
//
// The killed process is gone by the time the holder sees the kill. So while
// the memory category is on, the holder also takes a snapshot when the
// memory warning (holder_mem.go) starts an episode, and keeps the last one.
// At the kill, it writes both: the processes before, which name the one
// that the kernel stopped, and the processes left. With debug off, it takes
// no snapshot before, so the warning costs what it did.
//
// A snapshot reads /proc/<pid>/statm of every process, a single line, and
// then comm and smaps_rollup of the oomRead largest by RSS only.
// smaps_rollup walks each mapping of a process, so it is the slow part.

// oomRead is how many processes, the largest by RSS, a snapshot reads in
// full, and oomTop how many of them, the largest by PSS, go to holder.log.
const (
	oomRead = 20
	oomTop  = 10
)

// procSnap is a snapshot of the processes in the session's unit.
type procSnap struct {
	t     int64 // when it was taken, from diag.Now
	procs []diag.Proc
	all   int // the processes in the unit
}

// goSnapshot starts the snapshot after a kill, with kills the unit's count
// of kills. It reads the folders it needs first, so the goroutine reads no
// global. A holder outside its own unit takes no snapshot.
func (h *Holder) goSnapshot(kills int64) {
	if dir := h.mem.dir; dir != "" {
		go h.snapshotOOM(dir, procRoot, kills)
	}
}

// snapshotBefore takes a snapshot in the background when the memory
// warning has just started an episode, with on whether one was under way
// before this tick, and the memory category is on. It keeps it for the next
// kill. The caller holds h.mu.
func (h *Holder) snapshotBefore(on bool) {
	if on || !h.mem.on || !h.mem.mark || h.mem.dir == "" {
		return
	}
	if tr := h.dbg.tr.Load(); tr == nil || !tr.f.Cats().Has(diag.CatMemory) {
		return
	}
	dir, proc := h.mem.dir, procRoot
	go func() {
		procs, all := unitProcs(dir, proc)
		h.dbg.before.Store(&procSnap{t: diag.Now(), procs: procs, all: all})
	}()
}

// snapshotOOM writes the processes of the unit whose cgroup folder is dir:
// the largest oomTop by PSS to holder.log, and with the memory category on,
// the largest oomRead by RSS to the debug file, as an "oom" record, with
// the unit's memory. It writes the last snapshot before the kill with them,
// when there is one. proc is the folder of /proc.
func (h *Holder) snapshotOOM(dir, proc string, kills int64) {
	procs, all := unitProcs(dir, proc)
	before := h.dbg.before.Load()
	if before != nil {
		ago := time.Duration(diag.Now() - before.t).Round(time.Second)
		h.log.Warn("out-of-memory kill: the largest processes in the session's unit at the memory warning, before the kill", "ago", ago, "procs", before.all)
		logProcs(h.log, "out-of-memory kill: process before", before.procs)
	}
	h.log.Warn("out-of-memory kill: the largest processes left in the session's unit after the kill", "kills", kills, "procs", all)
	logProcs(h.log, "out-of-memory kill: process left", procs)
	tr := h.dbg.tr.Load()
	if tr == nil {
		return
	}
	r := diag.Record{T: diag.Now(), Cat: diag.CatMemory, Ev: "oom", N: all, Mem: readMemory(dir), Procs: procs}
	if before != nil {
		r.Before, r.BeforeT = before.procs, before.t
	}
	tr.f.Add(r)
}

// logProcs writes the largest oomTop of procs to log, one line each.
func logProcs(log *slog.Logger, msg string, procs []diag.Proc) {
	for i, p := range procs[:min(len(procs), oomTop)] {
		log.Warn(msg, "rank", i+1, "pid", p.PID, "comm", p.Comm, "pss", p.PSS, "rss", p.RSS)
	}
}

// unitProcs reads the oomRead largest processes by RSS in a unit's
// cgroup.procs: each one's name, and its RSS and PSS from smaps_rollup,
// largest PSS first. all counts the processes in the unit. A process that
// is gone by then is left out.
func unitProcs(dir, proc string) (procs []diag.Proc, all int) {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, 0
	}
	page := int64(os.Getpagesize())
	for _, line := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			continue
		}
		if rss, ok := readStatm(filepath.Join(proc, line, "statm"), page); ok {
			procs = append(procs, diag.Proc{PID: pid, RSS: rss})
		}
	}
	all = len(procs)
	sort.SliceStable(procs, func(i, j int) bool { return procs[i].RSS > procs[j].RSS })
	out := procs[:0]
	for _, p := range procs[:min(len(procs), oomRead)] {
		pid := strconv.Itoa(p.PID)
		comm, err := os.ReadFile(filepath.Join(proc, pid, "comm"))
		if err != nil {
			continue
		}
		p.Comm = strings.TrimSpace(string(comm))
		if rss, pss := readRollup(filepath.Join(proc, pid, "smaps_rollup")); rss > 0 {
			p.RSS, p.PSS = rss, pss
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PSS > out[j].PSS })
	return out, all
}

// readStatm reads a process's resident pages from /proc/<pid>/statm, its
// second number, as bytes.
func readStatm(path string, page int64) (rss int64, ok bool) {
	b, err := os.ReadFile(path) //nolint:gosec // G703: a file of /proc, by a process ID from cgroup.procs
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n * page, true
}

// readRollup reads the Rss and Pss lines of a smaps_rollup file, which the
// kernel writes in kB, as bytes.
func readRollup(path string) (rss, pss int64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "Rss:":
			rss = n << 10
		case "Pss:":
			pss = n << 10
		}
	}
	return rss, pss
}
