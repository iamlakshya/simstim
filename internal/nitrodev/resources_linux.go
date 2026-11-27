//go:build linux

package nitrodev

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func nativeCounter(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open native Nitro counter: %w", err)
	}
	defer file.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil || fs.Type != unix.SYSFS_MAGIC {
		return 0, fmt.Errorf("the Nitro counter is not native sysfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return 0, fmt.Errorf("the Nitro counter exceeds its bound")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("the Nitro counter is not a non-negative integer")
	}
	return value, nil
}

// ReadResources observes only the fixed kernel counter namespace. It creates
// no enclave, reserves no resource and changes no parent setting.
func ReadResources() (Resources, error) {
	var r Resources
	for _, field := range []struct {
		name   string
		target *int64
	}{
		{"pcie_slots", &r.Slots}, {"pcie_slots_in_use", &r.SlotsInUse}, {"pcie_slots_total", &r.SlotsTotal}, {"mem_pool/total", &r.MemoryTotal}, {"mem_pool/free", &r.MemoryFree},
	} {
		value, err := nativeCounter(Sysfs + "/" + field.name)
		if err != nil {
			return r, err
		}
		*field.target = value
	}
	online, err := os.ReadFile("/sys/devices/system/cpu/present")
	if err != nil {
		return r, fmt.Errorf("read native parent CPU list: %w", err)
	}
	r.ParentCPUs, err = CPUCount(string(online))
	return r, err
}

// CPUCount parses the kernel's canonical CPU list with bounded ranges and no
// duplicate or overlapping IDs. Unknown lists never become an empty budget.
func CPUCount(text string) (int64, error) {
	var count int64
	previous := int64(-1)
	for _, part := range strings.Split(strings.TrimSpace(text), ",") {
		lo, hi, rangePart := strings.Cut(part, "-")
		first, err := strconv.ParseInt(lo, 10, 32)
		if err != nil || first < 0 || first <= previous {
			return 0, fmt.Errorf("the parent CPU list is invalid")
		}
		last := first
		if rangePart {
			last, err = strconv.ParseInt(hi, 10, 32)
		}
		if err != nil || last < first || last > 1<<16 {
			return 0, fmt.Errorf("the parent CPU range is invalid")
		}
		count += last - first + 1
		previous = last
	}
	return count, nil
}
