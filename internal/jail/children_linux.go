package jail

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// restChildren is replaced only by a test stage that simulates a kernel
// without task children files, or a listing that races child adoption.
var restChildren = children

// children lists this process's direct children from each of its threads.
// Without CONFIG_PROC_CHILDREN, /proc/<pid>/stat still names each parent.
func children() []int { return childrenIn("/proc", os.Getpid()) }

func childrenIn(proc string, parent int) []int {
	tasks, err := os.ReadDir(filepath.Join(proc, "self/task"))
	complete := err == nil
	var out []int
	for _, t := range tasks {
		b, err := os.ReadFile(filepath.Join(proc, "self/task", t.Name(), "children"))
		if err != nil {
			complete = false
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil && n > 0 && n != parent {
				out = append(out, n)
			}
		}
	}
	if complete {
		return out
	}
	return processChildren(proc, parent)
}

// processChildren returns only direct, unreaped children. Their PIDs stay
// pinned until this init waits, so a stale process-table row cannot signal
// a reused PID belonging to another process.
func processChildren(proc string, parent int) []int {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || pid == parent {
			continue
		}
		b, err := os.ReadFile(filepath.Join(proc, e.Name(), "stat"))
		if err == nil && processParent(string(b)) == parent {
			out = append(out, pid)
		}
	}
	return out
}

func processParent(stat string) int {
	// comm can contain both whitespace and parentheses. The final ')' ends it.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return -1
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return -1
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return -1
	}
	return parent
}
