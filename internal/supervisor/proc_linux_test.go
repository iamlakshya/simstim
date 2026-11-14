package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// processGone reports whether pid has gone, or waits as a zombie.
func processGone(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}
