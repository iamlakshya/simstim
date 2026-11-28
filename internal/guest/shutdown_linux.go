//go:build linux

package guest

import (
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// microVMShutdownConfig configures clean microvm teardown before reboot(2).
type microVMShutdownConfig struct {
	Docker       *dockerManager
	SessionMount string
	StateMounts  []string
	Grace        time.Duration
}

// shutdownMicroVM stops optional guest daemons, terminates remaining non-PID-1
// processes cleanly, unmounts/flushes the session disk, calls sync(2), and
// powers off the microvm via reboot(LINUX_REBOOT_CMD_POWER_OFF) when running
// as PID 1 (isolation spec 6.6, 20.3).
func shutdownMicroVM(cfg microVMShutdownConfig) {
	if cfg.Docker != nil {
		cfg.Docker.Stop(3 * time.Second)
	}
	if os.Getpid() != 1 {
		return
	}
	grace := cfg.Grace
	if grace <= 0 {
		grace = 1500 * time.Millisecond
	}
	terminateRemainingProcesses(grace)
	unmountMicroVMFilesystems(cfg.SessionMount, cfg.StateMounts)
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

func terminateRemainingProcesses(grace time.Duration) {
	_ = unix.Kill(-1, unix.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		reapAllNonBlocking()
		if !hasOtherUserProcesses() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = unix.Kill(-1, unix.SIGKILL)
	killDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(killDeadline) {
		reapAllNonBlocking()
		if !hasOtherUserProcesses() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func reapAllNonBlocking() {
	var ws unix.WaitStatus
	for {
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

// hasOtherUserProcesses reports whether any non-PID-1 user-space process
// (excluding kernel threads, which have an empty /proc/<pid>/cmdline) exists.
func hasOtherUserProcesses() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && len(b) > 0 {
			return true
		}
	}
	return false
}

func unmountMicroVMFilesystems(sessionMount string, stateMounts []string) {
	// Unmount state bind mounts and /home/agent first, then /work, then the
	// underlying session ext4 mount (falling back to read-only remount if
	// --keep-system's overlay root still pins its superblock).
	for i := len(stateMounts) - 1; i >= 0; i-- {
		if stateMounts[i] != "" {
			_ = unix.Unmount(stateMounts[i], 0)
		}
	}
	_ = unix.Unmount("/home/agent", 0)
	_ = unix.Unmount("/work", 0)
	if sessionMount != "" {
		if err := unix.Unmount(sessionMount, 0); err != nil {
			// When --keep-system places overlay upperdir on the session disk,
			// remounting / read-only first releases writable references so the
			// session ext4 filesystem can remount read-only and flush its journal.
			_ = unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
			_ = unix.Mount("", sessionMount, "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
		}
	}
}
