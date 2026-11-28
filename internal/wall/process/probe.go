// Package process implements the W1 process wall on Linux using bubblewrap,
// Landlock and seccomp (isolation spec 5.1, decision 0053).
package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

const (
	// DriverName is the name of the process wall driver.
	DriverName = "process"

	procMaxUserNS          = "/proc/sys/user/max_user_namespaces"
	procUnprivUserNSClone  = "/proc/sys/kernel/unprivileged_userns_clone"
	procAppArmorUserNS     = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	procAppArmorUnconfined = "/proc/sys/kernel/apparmor_restrict_unprivileged_unconfined"
	etcOSRelease           = "/etc/os-release"
	appArmorBwrapRestrict  = "/etc/apparmor.d/bwrap-userns-restrict"
	appArmorBwrapPlain     = "/etc/apparmor.d/bwrap"
	appArmorBwrapExtra     = "/usr/share/apparmor/extra-profiles/bwrap-userns-restrict"
)

func init() {
	wall.Register(Driver{})
}

// Driver probes and builds W1 process walls backed by bubblewrap.
type Driver struct{}

// Name returns "process".
func (Driver) Name() string { return DriverName }

// Level returns wall.Process.
func (Driver) Level() wall.Level { return wall.Process }

// Probe checks whether this host can run bubblewrap process walls.
func (Driver) Probe(ctx context.Context) wall.Probe {
	return ProbeWith(ctx, defaultProbeDeps())
}

// Prepare validates spec, persists the sandbox mount plan under
// spec.SessionDir, and returns a bubblewrap-backed Sandbox ready to Start.
func (Driver) Prepare(_ context.Context, spec wall.Spec) (wall.Sandbox, error) {
	return prepareDriver(spec)
}

// Open reloads a previously prepared process-wall sandbox from sessionDir.
func (Driver) Open(_ context.Context, sessionDir string) (wall.Sandbox, error) {
	return openDriver(sessionDir)
}

// ProbeDeps injects host reads and commands so ProbeWith can run unit tests on
// any operating system with fake files and fake tools.
type ProbeDeps struct {
	GOOS          string
	KernelRelease func() string
	ReadFile      func(string) ([]byte, error)
	Stat          func(string) (os.FileInfo, error)
	LookPath      func(string) (string, error)
	LandlockABI   func() int
	SeccompOK     func() bool
	RunCmd        func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func defaultProbeDeps() ProbeDeps {
	return ProbeDeps{
		GOOS:          runtime.GOOS,
		KernelRelease: kernelRelease,
		ReadFile:      os.ReadFile,
		Stat:          os.Stat,
		LookPath:      exec.LookPath,
		LandlockABI:   landlockABI,
		SeccompOK:     seccompOK,
		RunCmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return out, fmt.Errorf("run %s: %w", name, err)
			}
			return out, nil
		},
	}
}

type probeState struct {
	release          string
	osInfo           osReleaseInfo
	abi              int
	secOK            bool
	maxUserNS        int
	hasMaxUserNS     bool
	unprivClone      int
	hasUnprivClone   bool
	appArmorRestrict bool
	profilePath      string
	hasProfile       bool
	hasExtraProfile  bool
	bwrapPath        string
	hasSystemBwrap   bool
	bwrapFound       bool
	bwrapOK          bool
	bwrapRunErr      string
}

// ProbeWith runs the process-wall probe using d.
func ProbeWith(ctx context.Context, d ProbeDeps) wall.Probe {
	facts := map[string]string{}
	p := wall.Probe{
		Driver: DriverName,
		Level:  wall.Process,
		Facts:  facts,
	}
	if d.GOOS != "linux" {
		p.Reason = "process walls require Linux"
		p.Fix = "use a Linux host, or run inside a Lima VM on macOS"
		return p
	}

	st := collectProbeState(ctx, d, &p)
	diagnoseProcessProbe(&p, st, d.LookPath)
	return p
}

func collectProbeState(ctx context.Context, d ProbeDeps, p *wall.Probe) probeState {
	facts := p.Facts
	st := probeState{
		release:          strings.TrimSpace(d.KernelRelease()),
		osInfo:           parseOSRelease(readString(d.ReadFile, etcOSRelease)),
		abi:              d.LandlockABI(),
		secOK:            d.SeccompOK(),
		appArmorRestrict: readIntEquals(d.ReadFile, procAppArmorUserNS, 1),
		hasExtraProfile:  pathExists(d.Stat, appArmorBwrapExtra),
		hasSystemBwrap:   pathExists(d.Stat, systemBwrapPath),
	}
	st.maxUserNS, st.hasMaxUserNS = readInt(d.ReadFile, procMaxUserNS)
	st.unprivClone, st.hasUnprivClone = readInt(d.ReadFile, procUnprivUserNSClone)
	if st.release != "" {
		facts["kernel"] = st.release
	}
	facts["landlock_abi"] = strconv.Itoa(st.abi)
	facts["seccomp"] = strconv.FormatBool(st.secOK)
	facts["apparmor_userns"] = strconv.FormatBool(st.appArmorRestrict)
	if readIntEquals(d.ReadFile, procAppArmorUnconfined, 1) {
		facts["apparmor_unconfined"] = "true"
	}

	st.profilePath = findAppArmorProfile(d.Stat)
	st.hasProfile = st.profilePath != ""
	if st.hasProfile {
		facts["bwrap_apparmor_profile"] = "installed"
	}

	probeBwrapState(ctx, d, p, &st)
	return st
}

func findAppArmorProfile(stat func(string) (os.FileInfo, error)) string {
	switch {
	case pathExists(stat, appArmorBwrapRestrict):
		return appArmorBwrapRestrict
	case pathExists(stat, appArmorBwrapPlain):
		return appArmorBwrapPlain
	default:
		return ""
	}
}

func probeBwrapState(ctx context.Context, d ProbeDeps, p *wall.Probe, st *probeState) {
	facts := p.Facts
	bwrapPath, bwrapErr := d.LookPath("bwrap")
	st.bwrapFound = bwrapErr == nil && bwrapPath != ""
	st.bwrapPath = bwrapPath
	var userNSOK bool

	if st.bwrapFound {
		facts["bwrap"] = bwrapPath
		if ver := probeBwrapVersion(ctx, d, bwrapPath); ver != "" {
			p.Version = ver
			facts["bwrap_version"] = ver
		}
		st.bwrapOK, st.bwrapRunErr = probeBwrapRun(ctx, d, bwrapPath)
		facts["bwrap_ok"] = strconv.FormatBool(st.bwrapOK)
		userNSOK = st.bwrapOK
		recordBwrapAppArmorFact(facts, st)
	} else {
		// Even without bwrap, check whether unprivileged user namespaces work
		// on this host so host doctor reports both facts clearly.
		if (!st.hasMaxUserNS || st.maxUserNS > 0) && (!st.hasUnprivClone || st.unprivClone != 0) {
			userNSOK = probeDirectUserNS(ctx, d)
		}
		if st.appArmorRestrict && !st.hasProfile {
			facts["bwrap_apparmor"] = "missing"
		}
	}
	facts["userns"] = strconv.FormatBool(userNSOK)
}

func recordBwrapAppArmorFact(facts map[string]string, st *probeState) {
	if st.bwrapOK {
		if st.appArmorRestrict {
			facts["bwrap_apparmor"] = "allowed"
		}
		return
	}
	if st.bwrapRunErr != "" {
		facts["bwrap_error"] = st.bwrapRunErr
	}
	if st.appArmorRestrict {
		facts["bwrap_apparmor"] = "blocked"
	}
}

func diagnoseProcessProbe(p *wall.Probe, st probeState, lookPath func(string) (string, error)) {
	switch {
	case kernelOlderThan513(st.release):
		p.Reason = fmt.Sprintf("kernel %s is older than Linux 5.13", st.release)
		p.Fix = "upgrade the host kernel to Linux 5.13 or later"
	case st.abi < 1:
		p.Reason = "Landlock is not enabled on this kernel"
		p.Fix = "boot a kernel with Landlock enabled (CONFIG_SECURITY_LANDLOCK and lsm=landlock)"
	case !st.secOK:
		p.Reason = "seccomp filters are not available"
		p.Fix = "run on a Linux host with CONFIG_SECCOMP_FILTER enabled (not inside a restricted container)"
	case !st.bwrapFound:
		p.Reason = "bubblewrap (bwrap) is not installed"
		p.Fix = missingBwrapFix(st.osInfo, lookPath, st.appArmorRestrict, st.hasProfile, st.hasExtraProfile)
	case !st.bwrapOK && st.hasMaxUserNS && st.maxUserNS == 0:
		p.Reason = "user namespaces are disabled (user.max_user_namespaces = 0)"
		p.Fix = userNSSysctlFix("user.max_user_namespaces", "15000")
	case !st.bwrapOK && st.hasUnprivClone && st.unprivClone == 0:
		p.Reason = "unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone = 0)"
		p.Fix = userNSSysctlFix("kernel.unprivileged_userns_clone", "1")
	case !st.bwrapOK && st.appArmorRestrict:
		p.Reason, p.Fix = appArmorBwrapDiagnosis(st, lookPath)
	case !st.bwrapOK:
		if st.bwrapRunErr != "" {
			p.Reason = "bubblewrap could not start a sandbox: " + st.bwrapRunErr
		} else {
			p.Reason = "bubblewrap could not create unprivileged user and mount namespaces"
		}
		p.Fix = "check that unprivileged user namespaces and mount namespaces are allowed on this host"
	default:
		p.OK = true
	}
}

func kernelOlderThan513(release string) bool {
	maj, minVal, ok := api.ParseKernelMajorMinor(release)
	return ok && (maj < 5 || (maj == 5 && minVal < 13))
}

// BwrapSmokeArgs returns the bubblewrap arguments used to verify that bwrap
// can create unprivileged user, mount, PID, net, IPC, UTS and cgroup
// namespaces, map uid/gid 1000, and mount /proc and tmpfs.
func BwrapSmokeArgs(trueBin string) []string {
	if trueBin == "" {
		trueBin = "/bin/true"
	}
	return []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-cgroup",
		"--uid", "1000",
		"--gid", "1000",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
		"--new-session",
		"--", trueBin,
	}
}

func probeBwrapVersion(ctx context.Context, d ProbeDeps, bwrapPath string) string {
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := d.RunCmd(tctx, bwrapPath, "--version")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 2 && strings.EqualFold(fields[0], "bubblewrap") {
		return fields[1]
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return ""
}

func probeBwrapRun(ctx context.Context, d ProbeDeps, bwrapPath string) (bool, string) {
	trueBin, err := d.LookPath("true")
	if err != nil || trueBin == "" {
		trueBin = "/bin/true"
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.RunCmd(tctx, bwrapPath, BwrapSmokeArgs(trueBin)...)
	if err == nil {
		return true, ""
	}
	msg := firstNonEmptyLine(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return false, msg
}

func probeDirectUserNS(ctx context.Context, d ProbeDeps) bool {
	unsharePath, err := d.LookPath("unshare")
	if err != nil || unsharePath == "" {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// unshare -Urm creates user and mount namespaces mapped to root and mounts
	// a tmpfs inside them: on Ubuntu with apparmor_restrict_unprivileged_userns=1,
	// unshare succeeds but mounting tmpfs fails with EPERM.
	_, err = d.RunCmd(tctx, unsharePath, "-Urm", "sh", "-c", "mount -t tmpfs none /tmp")
	return err == nil
}

type osReleaseInfo struct {
	ID        string
	IDLike    string
	VersionID string
}

func parseOSRelease(data string) osReleaseInfo {
	var info osReleaseInfo
	for _, line := range strings.Split(data, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			info.ID = strings.ToLower(v)
		case "ID_LIKE":
			info.IDLike = strings.ToLower(v)
		case "VERSION_ID":
			info.VersionID = v
		}
	}
	return info
}

func readString(readFile func(string) ([]byte, error), path string) string {
	if readFile == nil {
		return ""
	}
	b, err := readFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readInt(readFile func(string) ([]byte, error), path string) (int, bool) {
	s := strings.TrimSpace(readString(readFile, path))
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func readIntEquals(readFile func(string) ([]byte, error), path string, want int) bool {
	v, ok := readInt(readFile, path)
	return ok && v == want
}

func pathExists(stat func(string) (os.FileInfo, error), path string) bool {
	if stat == nil {
		return false
	}
	_, err := stat(path)
	return err == nil
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
