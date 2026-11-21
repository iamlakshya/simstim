//go:build darwin

package none

import "syscall"

func unwalledSocketPairV1() ([2]int, error) {
	return [2]int{}, failure("the unwalled supervisor needs a Linux host", nil)
}

// The original unreaped child and proc's kqueue/WAITID observation retain
// identity on Darwin. No saved PID or namespace is accepted here.
func unwalledAttributes(*int) *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
func closeUnwalledHandle(int) error                { return nil }
func checkUnwalledHandle(int) error                { return nil }
func unwalledBirthV1(int) (string, error)          { return "", nil }
