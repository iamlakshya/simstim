//go:build linux

package process

import (
	"errors"
	"fmt"
	"slices"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// landlockCreateRulesetVersion is LANDLOCK_CREATE_RULESET_VERSION (1 << 0).
const landlockCreateRulesetVersion = 1

// landlockKernelABI returns the running kernel's Landlock ABI version, or 0
// when Landlock is disabled or unsupported.
func landlockKernelABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// ApplyLandlock enforces filesystem (and, on ABI 6+, IPC scope) confinement on
// the current process and all future children using github.com/landlock-lsm/go-landlock.
// It fails closed if Landlock is unavailable on the running kernel.
func ApplyLandlock(cfg LandlockConfig) error {
	abi := landlockKernelABI()
	if abi < 1 {
		return errors.New("landlock is not enabled on this kernel. Try: boot a Linux 5.13 or later kernel with Landlock enabled")
	}

	var rules []landlock.Rule
	roDirs := dedupPaths(cfg.RODirs)
	if len(roDirs) > 0 {
		rules = append(rules, landlock.RODirs(roDirs...).IgnoreIfMissing())
	}

	rwDirs := dedupPaths(cfg.RWDirs)
	if len(rwDirs) > 0 {
		r := landlock.RWDirs(rwDirs...).IgnoreIfMissing()
		if abi >= 2 {
			r = r.WithRefer()
		}
		rules = append(rules, r)
	}

	// Minimal character devices created by bwrap --dev /dev. PTY allocation
	// (creack/pty) issues ioctl(2) on /dev/ptmx and /dev/pts/*, which Landlock
	// ABI V5+ restricts unless WithIoctlDev() is granted on those device nodes.
	stdDevFiles := []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom"}
	rules = append(rules, landlock.RWFiles(stdDevFiles...).IgnoreIfMissing())

	ptyFiles := landlock.RWFiles("/dev/ptmx", "/dev/tty").IgnoreIfMissing()
	ptsDir := landlock.RWDirs("/dev/pts").IgnoreIfMissing()
	if abi >= 5 {
		ptyFiles = ptyFiles.WithIoctlDev()
		ptsDir = ptsDir.WithIoctlDev()
	}
	rules = append(rules, ptyFiles, ptsDir)

	shmDir := landlock.RWDirs("/dev/shm").IgnoreIfMissing()
	if abi >= 2 {
		shmDir = shmDir.WithRefer()
	}
	rules = append(rules, shmDir)

	rwFiles := dedupPaths(cfg.RWFiles)
	if len(rwFiles) > 0 {
		rules = append(rules, landlock.RWFiles(rwFiles...).IgnoreIfMissing())
	}

	// Enforce filesystem paths and (on ABI 6+) IPC scoping in ONE Landlock
	// ruleset. Stacking a second RestrictScoped() layer with HandledAccessFS=0
	// implicitly blocks cross-directory renames (EXDEV) on Linux 6.12 even when
	// the first layer grants WithRefer(). Keep HandledAccessNet empty so the
	// guest loopback proxy can bind 127.0.0.1:3128 inside its isolated netns.
	llCfg := landlock.Config{
		HandledAccessFS: landlock.V6.HandledAccessFS,
		Scoped:          landlock.V6.Scoped,
	}.BestEffort()
	if err := llCfg.Restrict(rules...); err != nil {
		return fmt.Errorf("apply landlock rules: %w. Try: check the kernel Landlock support with host doctor", err)
	}
	return nil
}

func dedupPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
