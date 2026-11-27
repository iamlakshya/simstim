package host_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func ownedHolderState(pid int) (string, bool, error) {
	if pid <= 0 {
		return "", false, fmt.Errorf("invalid owned holder PID")
	}
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect the owned holder: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return "", false, fmt.Errorf("the owned holder has a different process owner")
	}
	f, err := os.Open(filepath.Join(dir, "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the owned holder state: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return "", false, fmt.Errorf("read the owned holder state: %w", err)
	}
	if len(raw) > 65536 {
		return "", false, fmt.Errorf("the owned holder state is too large")
	}
	return parseOwnedHolderStat(pid, string(raw))
}

func parseOwnedHolderStat(pid int, raw string) (string, bool, error) {
	open, end := strings.IndexByte(raw, '('), strings.LastIndexByte(raw, ')')
	if open <= 0 || end <= open {
		return "", false, fmt.Errorf("the owned holder state has no command boundary")
	}
	got, err := strconv.Atoi(strings.TrimSpace(raw[:open]))
	if err != nil || got != pid {
		return "", false, fmt.Errorf("the owned holder state has a different PID")
	}
	fields := strings.Fields(raw[end+1:])
	// The suffix starts at field 3. Field 22 is the native start tick count.
	if len(fields) <= 19 || len(fields[0]) != 1 {
		return "", false, fmt.Errorf("the owned holder state is incomplete")
	}
	birth, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || birth == 0 {
		return "", false, fmt.Errorf("the owned holder state has no native birth ticks")
	}
	return strconv.FormatUint(birth, 10), fields[0] != "Z" && fields[0] != "X", nil
}
