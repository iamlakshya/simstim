//go:build linux

package host

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func hostFactsDiskV1(path string) (int64, bool) {
	var facts unix.Statfs_t
	if unix.Statfs(path, &facts) != nil || facts.Bsize <= 0 || facts.Bavail > uint64(math.MaxInt64)/uint64(facts.Bsize) {
		return 0, false
	}
	return int64(facts.Bavail) * facts.Bsize, true
}

func hostFactsPressureV1(kind string) (float64, bool) {
	if kind != "memory" && kind != "cpu" {
		return 0, false
	}
	data, ok := readHostFactsNativeV1(filepath.Join("/proc/pressure", kind), 4096)
	if !ok {
		return 0, false
	}
	return parseHostFactsPressureV1(data)
}

func parseHostFactsPressureV1(data []byte) (float64, bool) {
	var value float64
	found := false
	someSeen := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		if someSeen {
			return 0, false
		}
		someSeen = true
		for _, field := range fields[1:] {
			text, ok := strings.CutPrefix(field, "avg10=")
			if !ok {
				continue
			}
			if found {
				return 0, false
			}
			parsed, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 100 {
				return 0, false
			}
			value, found = parsed, true
		}
	}
	return value, found
}

func readHostFactsNativeV1(path string, limit int) ([]byte, bool) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		_ = unix.Close(fd)
		return nil, false
	}
	file := os.NewFile(uintptr(fd), "native-host-facts")
	data, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	return data, readErr == nil && closeErr == nil && len(data) <= limit
}

func hostFactsBatteryV1() (bool, bool) {
	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil {
		return false, false
	}
	battery := false
	discharging := false
	for _, entry := range entries {
		// These are kernel sysfs links. No configured path or supplied payload
		// selects an outside power source or a caller-owned filesystem object.
		base := filepath.Join("/sys/class/power_supply", entry.Name())
		kind, err := os.ReadFile(filepath.Join(base, "type"))
		if err != nil {
			return false, false
		}
		if strings.TrimSpace(string(kind)) != "Battery" {
			continue
		}
		battery = true
		status, err := os.ReadFile(filepath.Join(base, "status"))
		if err != nil {
			return false, false
		}
		switch strings.TrimSpace(string(status)) {
		case "Discharging":
			discharging = true
		case "Charging", "Full":
		default:
			return false, false
		}
	}
	if !battery {
		return false, true
	}
	return discharging, true
}
