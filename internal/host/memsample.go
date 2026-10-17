package host

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/diag"
)

// unitCgroupDir is the cgroup folder of the unit that launchHolder started
// for session id, when the holder runs in it, or "".
func unitCgroupDir(id string) string {
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok && filepath.Base(rest) == UnitName(id) {
			return filepath.Join(cgroupRoot, rest)
		}
	}
	return ""
}

// readMemory reads one sample of the memory files in a cgroup folder. A
// file the kernel does not have (memory.peak before Linux 5.19) reads as 0.
func readMemory(dir string) *diag.Memory {
	stat := readKeyed(filepath.Join(dir, "memory.stat"))
	events := readKeyed(filepath.Join(dir, "memory.events"))
	m := &diag.Memory{
		Current:  readInt(filepath.Join(dir, "memory.current")),
		Peak:     readInt(filepath.Join(dir, "memory.peak")),
		Anon:     stat["anon"],
		File:     stat["file"],
		Refault:  stat["workingset_refault_file"],
		MajFault: stat["pgmajfault"],
		High:     events["high"],
		Max:      events["max"],
		OOM:      events["oom"],
		OOMKill:  events["oom_kill"],
	}
	mem := readPressure(filepath.Join(dir, "memory.pressure"))
	cpu := readPressure(filepath.Join(dir, "cpu.pressure"))
	m.MemSome, m.MemFull = mem.some.ns(), mem.full.ns()
	m.CPUSome, m.CPUFull = cpu.some.ns(), cpu.full.ns()
	return m
}

// readInt reads a file that holds one number.
func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// readKeyed reads a file of "name value" lines, as memory.stat is.
func readKeyed(path string) map[string]int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]int64{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				out[k] = n
			}
		}
	}
	return out
}

// pressure is what a pressure file says (Linux PSI) about the time in
// which some of the tasks, or all of them ("full"), stalled.
type pressure struct {
	some, full stall
}

// stall is one line of a pressure file.
type stall struct {
	avg10 float64 // the share of the last 10 s, in %
	total int64   // all the stall time so far, which the kernel counts in µs
}

// ns is all the stall time so far, in ns.
func (s stall) ns() int64 { return s.total * 1000 }

// readPressure reads a pressure file. A file that is missing reads as no
// stall.
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=6789
func readPressure(path string) pressure {
	var p pressure
	b, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	for _, line := range strings.Split(string(b), "\n") {
		kind, rest, _ := strings.Cut(line, " ")
		var s stall
		for _, f := range strings.Fields(rest) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "avg10":
				s.avg10, _ = strconv.ParseFloat(v, 64)
			case "total":
				s.total, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		switch kind {
		case "some":
			p.some = s
		case "full":
			p.full = s
		}
	}
	return p
}
