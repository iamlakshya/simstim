package host

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// pidAlive reads a live process of this user without consuming a child exit.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(b), ')')
	fields := strings.Fields(string(b[end+1:]))
	return end >= 0 && len(fields) > 0 && len(fields[0]) == 1 && fields[0] != "Z" && fields[0] != "X"
}

func holderProcessArgs(pid int) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, fmt.Errorf("read holder process arguments: %w", err)
	}
	return strings.Split(string(bytes.TrimSuffix(b, []byte{0})), "\x00"), nil
}
