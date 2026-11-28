//go:build linux

package jail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

type execConfig struct {
	TargetPath  string     `json:"target_path"`
	Argv        []string   `json:"argv"`
	Env         []string   `json:"env"`
	Net         NetMode    `json:"net"`
	Binds       []Bind     `json:"binds,omitempty"`
	Rules       []PathRule `json:"rules,omitempty"`
	Profile     Profile    `json:"profile,omitempty"`
	InMountNS   bool       `json:"in_mount_ns,omitempty"`
	SkipSeccomp bool       `json:"skip_seccomp,omitempty"`
}

// landlockKernelABI returns the running kernel's Landlock ABI version, or 0
// when Landlock is disabled or unsupported.
func landlockKernelABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// RunExec implements the internal `jail-exec --config-fd <fd>` trampoline. It
// reads its confinement plan from the given file descriptor, closes that fd,
// locks itself to one OS thread, clears dumpability and core dumps, enforces
// Landlock and seccomp, and replaces itself with the target binary via execve.
// It never calls setgroups.
func RunExec(args []string) error {
	if len(args) != 2 || args[0] != "--config-fd" {
		return errors.New("jail-exec: expected --config-fd <fd>. Try: invoke commands through internal/jail.Command")
	}
	fdNum, err := strconv.Atoi(args[1])
	if err != nil || fdNum < 3 {
		return fmt.Errorf("jail-exec: invalid config fd %q. Try: invoke commands through internal/jail.Command", args[1])
	}
	cfgFile := os.NewFile(uintptr(fdNum), "jail-config")
	if cfgFile == nil {
		return errors.New("jail-exec: config fd is not open. Try: invoke commands through internal/jail.Command")
	}
	rawCfg, readErr := io.ReadAll(io.LimitReader(cfgFile, 1<<20))
	_ = cfgFile.Close()
	if readErr != nil {
		return fmt.Errorf("jail-exec: read config: %w", readErr)
	}
	var cfg execConfig
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		return fmt.Errorf("jail-exec: decode config: %w", err)
	}
	if cfg.TargetPath == "" || len(cfg.Argv) == 0 {
		return errors.New("jail-exec: missing target command in config")
	}

	// Keep all confinement syscalls and unix.Exec on the same OS thread.
	runtime.LockOSThread()

	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("jail-exec: disable core dumps: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("jail-exec: clear dumpable flag: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("jail-exec: set PR_SET_NO_NEW_PRIVS: %w", err)
	}

	if err := applyJailLandlock(cfg); err != nil {
		return err
	}

	if !cfg.SkipSeccomp {
		rawBPF, err := SeccompInstructions(cfg.Profile, cfg.Net)
		if err != nil {
			return err
		}
		if err := installSeccompBPF(rawBPF); err != nil {
			return err
		}
	}

	if err := unix.Exec(cfg.TargetPath, cfg.Argv, cfg.Env); err != nil {
		return fmt.Errorf("jail-exec: exec %s: %w", cfg.TargetPath, err)
	}
	return nil
}

func applyJailLandlock(cfg execConfig) error {
	abi := landlockKernelABI()
	if !cfg.InMountNS {
		if err := checkNoUserNSLandlockABI(abi); err != nil {
			return err
		}
	} else if abi < 1 {
		return errors.New("jail-exec: Landlock is not enabled on this kernel. Try: boot Linux 5.13 or later with Landlock enabled")
	}

	var rules []landlock.Rule
	// Read-only system runtime libraries and binaries.
	rules = append(rules, landlock.RODirs("/usr", "/bin", "/sbin", "/lib", "/lib64").IgnoreIfMissing())

	// Minimal /etc entries for dynamic linking (and DNS/TLS for NetHost).
	etcPaths := baseEtcPaths
	if cfg.Net == NetHost {
		etcPaths = slices.Concat(baseEtcPaths, netHostEtcPaths)
	}
	rules = append(rules, pathRulesForList(etcPaths, false, false, abi)...)

	// The target binary itself (and its resolved symlink target if any).
	rules = append(rules, landlock.ROFiles(cfg.TargetPath).IgnoreIfMissing())
	if realTarget, err := filepath.EvalSymlinks(cfg.TargetPath); err == nil && realTarget != "" && realTarget != cfg.TargetPath {
		rules = append(rules, landlock.ROFiles(realTarget).IgnoreIfMissing())
	}

	// Allow reading only /proc/self (never /proc/<supervisor>/mem).
	rules = append(rules, landlock.RODirs("/proc/self").IgnoreIfMissing())

	// Standard minimal character devices.
	rules = append(rules, landlock.RWFiles(
		"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom",
	).IgnoreIfMissing())

	// Inside a mount namespace, /tmp is a private tmpfs mounted by bwrap.
	if cfg.InMountNS {
		tmpRule := landlock.RWDirs("/tmp").IgnoreIfMissing()
		if abi >= 2 {
			tmpRule = tmpRule.WithRefer()
		}
		rules = append(rules, tmpRule)
	}

	for _, b := range cfg.Binds {
		p := b.Src
		if cfg.InMountNS && b.Dest != "" {
			p = b.Dest
		}
		switch b.Mode {
		case BindRO:
			rules = append(rules, pathRulesForList([]string{p}, false, false, abi)...)
		case BindRW:
			rules = append(rules, pathRulesForList([]string{p}, true, false, abi)...)
		case BindDev:
			rules = append(rules, pathRulesForList([]string{p}, true, true, abi)...)
		}
	}

	for _, r := range cfg.Rules {
		rules = append(rules, pathRulesForList([]string{r.Path}, r.Write, r.IoctlDev, abi)...)
	}

	// Apply filesystem, IPC scope (ABI 6+) and TCP restriction (ABI 4+ for
	// NetNone) in a single ruleset so cross-directory renames (WithRefer) keep
	// working cleanly.
	llCfg := landlock.Config{
		HandledAccessFS: landlock.V6.HandledAccessFS,
		Scoped:          landlock.V6.Scoped,
	}
	if cfg.Net == NetNone {
		llCfg.HandledAccessNet = landlock.V4.HandledAccessNet
	}
	if err := llCfg.BestEffort().Restrict(rules...); err != nil {
		return fmt.Errorf("jail-exec: apply Landlock rules: %w. Try: check kernel Landlock support with host doctor", err)
	}
	return nil
}

func pathRulesForList(paths []string, write, ioctlDev bool, abi int) []landlock.Rule {
	var out []landlock.Rule
	for _, p := range paths {
		if p == "" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		var r landlock.FSRule
		if fi.IsDir() {
			if write {
				r = landlock.RWDirs(p).IgnoreIfMissing()
				if abi >= 2 {
					r = r.WithRefer()
				}
			} else {
				r = landlock.RODirs(p).IgnoreIfMissing()
			}
		} else {
			if write {
				r = landlock.RWFiles(p).IgnoreIfMissing()
			} else {
				r = landlock.ROFiles(p).IgnoreIfMissing()
			}
		}
		if ioctlDev && abi >= 5 {
			r = r.WithIoctlDev()
		}
		out = append(out, r)
	}
	return out
}
