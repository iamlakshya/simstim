//go:build linux

package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func localSystem() probeSystem {
	var uname unix.Utsname
	kernel := ""
	if unix.Uname(&uname) == nil {
		for _, c := range uname.Release {
			if c == 0 {
				break
			}
			kernel += string(byte(c))
		}
	}
	return probeSystem{os: runtime.GOOS, kernel: kernel, readFile: os.ReadFile, landlock: landlockABI, smoke: launcherSmoke}
}

func landlockABI() int {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || version > 1<<16 {
		return 0
	}
	return int(version)
}

func namespaceSmoke(ctx context.Context) smokeResult {
	exe, err := os.Executable()
	if err != nil {
		return smokeResult{Error: "find helper executable"}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "wall", "probe-child")
	// Map the caller to root only inside this child's user namespace.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	b, err := cmd.Output()
	if err != nil {
		return smokeResult{Error: "start namespace smoke child"}
	}
	var result smokeResult
	if err := json.Unmarshal(b, &result); err != nil {
		return smokeResult{Error: "read namespace smoke result"}
	}
	return result
}

func ownNamespace() bool {
	// The hidden command must be safe even when someone invokes it directly.
	// A single mapping from namespace root to one host user proves that
	// this is not the host's initial user namespace.
	b, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	var inside, outside, count int
	if _, err := fmt.Sscanf(string(b), "%d %d %d", &inside, &outside, &count); err != nil || inside != 0 || outside < 0 || count != 1 {
		return false
	}
	selfMount, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false
	}
	parentMount, err := os.Readlink("/proc/" + strconv.Itoa(os.Getppid()) + "/ns/mnt")
	return err == nil && parentMount != selfMount
}
