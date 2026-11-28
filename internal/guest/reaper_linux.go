//go:build linux

package guest

import (
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// startReaper installs a SIGCHLD orphan reaper when guest init runs as PID 1.
// It inspects /proc for zombie children reparented to PID 1 and only reaps PIDs
// that are not managed exec.Cmd holders, so it never races with cmd.Wait.
func startReaper(isManaged func(pid int) bool) func() {
	if os.Getpid() != 1 {
		return func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				reapOrphans(isManaged)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func reapOrphans(isManaged func(pid int) bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || isManaged(pid) {
			continue
		}
		if isPID1Zombie(pid) {
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		}
	}
}

func isPID1Zombie(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	// Format: "<pid> (<comm>) <state> <ppid> ..."
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	fields := strings.Fields(string(b[i+2:]))
	return len(fields) >= 2 && fields[0] == "Z" && fields[1] == "1"
}
