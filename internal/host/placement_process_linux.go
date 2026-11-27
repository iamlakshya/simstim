//go:build linux

package host

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func placementProcessBirth(pid int) (string, error) {
	if pid <= 0 {
		return "", placementError("the active operation has an invalid process identity")
	}
	base := "/proc/" + strconv.Itoa(pid)
	before, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	_, _, uid, _, ok := footprintIdentity(before)
	if !ok || uid != uint64(os.Getuid()) {
		return "", placementError("the active operation belongs to another account")
	}
	data, err := os.ReadFile(base + "/stat")
	if err != nil {
		return "", err
	}
	close := strings.LastIndex(string(data), ") ")
	if close < 0 || !strings.HasPrefix(string(data), strconv.Itoa(pid)+" (") {
		return "", placementError("the kernel process identity is malformed")
	}
	fields := strings.Fields(string(data)[close+2:])
	if len(fields) <= 19 || len(fields[0]) != 1 {
		return "", placementError("the kernel process identity is incomplete")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", fmt.Errorf("the captured process has ended: %w", os.ErrNotExist)
	}
	birth, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || strconv.FormatUint(birth, 10) != fields[19] {
		return "", placementError("the kernel process birth identity is invalid")
	}
	after, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) {
		return "", placementError("the active process changed during inspection")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", placementError("cannot bind the active process to this boot")
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(string(boot)), birth), nil
}
