package guest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GuestMemSample holds the guest's view of its own memory and cgroup OOM kills
// (emitted as untrusted ctl "mem" claims; spec 10.3).
type GuestMemSample struct {
	Used      int64
	Total     int64
	Available int64
	OOMKills  int64
}

// readGuestMem parses /proc/meminfo (meminfoPath) and optionally a cgroup v2
// memory.events file (cgroupDir) to build a ctl "mem" event payload.
func readGuestMem(meminfoPath, cgroupDir string) (GuestMemSample, bool) {
	if meminfoPath == "" {
		meminfoPath = "/proc/meminfo"
	}
	raw, err := os.ReadFile(meminfoPath)
	if err != nil {
		return GuestMemSample{}, false
	}
	var totalKB, availKB, freeKB int64
	var hasTotal, hasAvail bool
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || val < 0 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB, hasTotal = val, true
		case "MemAvailable:":
			availKB, hasAvail = val, true
		case "MemFree:":
			freeKB = val
		}
	}
	if !hasTotal || totalKB <= 0 {
		return GuestMemSample{}, false
	}
	if !hasAvail {
		availKB = freeKB
	}
	total := totalKB * 1024
	avail := min(availKB*1024, total)
	used := max(total-avail, 0)

	var oomKills int64
	evPath := filepath.Join(cgroupDir, "memory.events")
	if cgroupDir == "" {
		evPath = filepath.Join(defaultAgentCgroupDir, "memory.events")
		if _, err := os.Stat(evPath); err != nil {
			evPath = filepath.Join(defaultGuestCgroupRoot, "memory.events")
		}
	}
	if evRaw, err := os.ReadFile(evPath); err == nil {
		for _, line := range strings.Split(string(evRaw), "\n") {
			if rest, ok := strings.CutPrefix(line, "oom_kill "); ok {
				if n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil && n > 0 {
					oomKills = n
				}
			}
		}
	}
	return GuestMemSample{
		Used:      used,
		Total:     total,
		Available: avail,
		OOMKills:  oomKills,
	}, true
}
