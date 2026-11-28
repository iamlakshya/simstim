//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// computeAgentMemoryCap computes (memory.max, memory.high) for the guest's
// /sys/fs/cgroup/agent cgroup so a memory-hungry agent is OOM-killed inside
// its own cgroup before the guest kernel touches PID 1 or the holder
// (isolation spec 20.1).
func computeAgentMemoryCap(memTotal int64) (memMax, memHigh int64) {
	const (
		minTotalForCap = 128 << 20
		minReserve     = 64 << 20
		maxReserve     = 256 << 20
	)
	if memTotal <= minTotalForCap {
		return 0, 0
	}
	reserve := min(max(memTotal/8, minReserve), maxReserve)
	memMax = memTotal - reserve
	if memMax < minReserve {
		return 0, 0
	}
	memHigh = memMax - memMax/16
	return memMax, memHigh
}

// configureGuestCgroups sets up cgroup v2 memory accounting inside a microvm:
// moves PID 1 (and therefore holders spawned by it) into <root>/init, enables
// +memory in <root>/cgroup.subtree_control, creates <root>/agent (root-owned
// 0755 so uid 1000 can inspect memory.events but cannot raise its own limits),
// and caps memory.max / memory.high below MemTotal.
func configureGuestCgroups(cgroupRoot, meminfoPath string, selfPID int) (string, error) {
	if cgroupRoot == "" {
		cgroupRoot = defaultGuestCgroupRoot
	}
	initDir := filepath.Join(cgroupRoot, "init")
	//nolint:gosec // G301: cgroup dirs are 0755 root:root so uid 1000 cannot modify limits
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		return "", fmt.Errorf("create init cgroup %s: %w", initDir, err)
	}
	if selfPID > 0 {
		//nolint:gosec // G306: cgroup control files use 0644
		_ = os.WriteFile(filepath.Join(initDir, "cgroup.procs"), []byte(strconv.Itoa(selfPID)+"\n"), 0o644)
	}
	//nolint:gosec // G306: cgroup control files use 0644
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+memory\n"), 0o644); err != nil {
		return "", fmt.Errorf("enable memory controller in %s: %w", cgroupRoot, err)
	}
	agentDir := filepath.Join(cgroupRoot, "agent")
	//nolint:gosec // G301: cgroup dirs are 0755 root:root so uid 1000 cannot modify limits
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent cgroup %s: %w", agentDir, err)
	}
	if sample, ok := readGuestMem(meminfoPath, agentDir); ok {
		if memMax, memHigh := computeAgentMemoryCap(sample.Total); memMax > 0 {
			//nolint:gosec // G306: cgroup control files use 0644
			_ = os.WriteFile(filepath.Join(agentDir, "memory.max"), []byte(strconv.FormatInt(memMax, 10)+"\n"), 0o644)
			//nolint:gosec // G306: cgroup control files use 0644
			_ = os.WriteFile(filepath.Join(agentDir, "memory.high"), []byte(strconv.FormatInt(memHigh, 10)+"\n"), 0o644)
		}
	}
	return agentDir, nil
}
