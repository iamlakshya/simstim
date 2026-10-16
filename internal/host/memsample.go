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
	m.MemSome, m.MemFull = readPressure(filepath.Join(dir, "memory.pressure"))
	m.CPUSome, m.CPUFull = readPressure(filepath.Join(dir, "cpu.pressure"))
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

// readPressure reads the "total" stall times of a pressure file, which the
// kernel counts in µs, as ns:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=6789
func readPressure(path string) (some, full int64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		kind, rest, _ := strings.Cut(line, " ")
		var total int64
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "total="); ok {
				total, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		switch kind {
		case "some":
			some = total * 1000
		case "full":
			full = total * 1000
		}
	}
	return some, full
}
