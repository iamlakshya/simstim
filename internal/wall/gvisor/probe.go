package gvisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

const (
	// DriverName is the name of the gVisor wall driver (spec 21.4).
	DriverName = "gvisor"

	procMaxUserNS         = "/proc/sys/user/max_user_namespaces"
	procUnprivUserNSClone = "/proc/sys/kernel/unprivileged_userns_clone"
	procAppArmorUserNS    = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
)

func init() {
	wall.Register(Driver{})
}

// Driver probes and builds W2 gVisor walls backed by pinned upstream runsc.
type Driver struct{}

// Name returns "gvisor".
func (Driver) Name() string { return DriverName }

// Level returns wall.GVisor.
func (Driver) Level() wall.Level { return wall.GVisor }

// Probe checks whether this host can run gVisor walls with upstream runsc.
func (Driver) Probe(ctx context.Context) wall.Probe {
	return ProbeWith(ctx, defaultProbeDeps())
}

// Prepare validates spec, persists gvisor-wall.json under spec.SessionDir, and
// returns a Sandbox ready to Start.
func (Driver) Prepare(_ context.Context, spec wall.Spec) (wall.Sandbox, error) {
	return prepareDriver(spec)
}

// Open reloads a previously prepared gVisor sandbox from sessionDir.
func (Driver) Open(_ context.Context, sessionDir string) (wall.Sandbox, error) {
	return openDriver(sessionDir)
}

// ProbeDeps injects host reads and commands so ProbeWith can run unit tests on
// any operating system with fake files and fake runsc binaries.
type ProbeDeps struct {
	GOOS          string
	GOARCH        string
	KernelRelease func() string
	ReadFile      func(string) ([]byte, error)
	Stat          func(string) (os.FileInfo, error)
	LookPath      func(string) (string, error)
	FindRunsc     func(arch string) (string, error)
	RunCmd        func(ctx context.Context, name string, args ...string) ([]byte, error)
	SmokeRun      func(ctx context.Context, runscPath string) error
}

func defaultProbeDeps() ProbeDeps {
	return ProbeDeps{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		KernelRelease: kernelRelease,
		ReadFile:      os.ReadFile,
		Stat:          os.Stat,
		LookPath:      exec.LookPath,
		FindRunsc:     FindPinnedRunsc,
		RunCmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return out, fmt.Errorf("run %s: %w", name, err)
			}
			return out, nil
		},
		SmokeRun: runSmokeTest,
	}
}

type probeState struct {
	release             string
	maxUserNS           int
	hasMaxUserNS        bool
	unprivClone         int
	hasUnprivClone      bool
	appArmorRestrict    bool
	hasProfile          bool
	profileText         string
	profileMatchesRunsc bool
	runscPath           string
	runscErr            error
	smokeOK             bool
	smokeErr            string
}

// ProbeWith runs the gVisor wall probe using d (isolation spec 21.4).
func ProbeWith(ctx context.Context, d ProbeDeps) wall.Probe {
	facts := map[string]string{"platform": "systrap"}
	p := wall.Probe{
		Driver: DriverName,
		Level:  wall.GVisor,
		Facts:  facts,
	}
	if d.GOOS != "linux" {
		p.Reason = "gVisor walls require Linux"
		p.Fix = "use a Linux host, or run inside a Lima VM on macOS"
		return p
	}
	if d.GOARCH != "amd64" && d.GOARCH != "arm64" {
		p.Reason = fmt.Sprintf("gVisor walls require amd64 or arm64 (this host is %s)", d.GOARCH)
		p.Fix = "use an amd64 or arm64 Linux host"
		return p
	}

	st := collectProbeState(ctx, d, &p)
	diagnoseProbe(&p, st)
	return p
}

func collectProbeState(ctx context.Context, d ProbeDeps, p *wall.Probe) probeState {
	facts := p.Facts
	st := probeState{
		release:          strings.TrimSpace(d.KernelRelease()),
		appArmorRestrict: readIntEquals(d.ReadFile, procAppArmorUserNS, 1),
	}
	if d.ReadFile != nil {
		if raw, err := d.ReadFile(AppArmorProfilePath()); err == nil {
			st.hasProfile = true
			st.profileText = string(raw)
		}
	}
	st.maxUserNS, st.hasMaxUserNS = readInt(d.ReadFile, procMaxUserNS)
	st.unprivClone, st.hasUnprivClone = readInt(d.ReadFile, procUnprivUserNSClone)

	if st.release != "" {
		facts["kernel"] = st.release
	}
	facts["apparmor_userns"] = strconv.FormatBool(st.appArmorRestrict)
	if st.hasProfile {
		facts["runsc_apparmor_profile"] = "installed"
	}

	if d.FindRunsc != nil {
		st.runscPath, st.runscErr = d.FindRunsc(d.GOARCH)
	}
	if st.runscErr != nil || st.runscPath == "" {
		return st
	}
	facts["runsc"] = st.runscPath
	if st.hasProfile {
		st.profileMatchesRunsc = profileMatchesRunsc(st.profileText, st.runscPath)
		if !st.profileMatchesRunsc {
			facts["runsc_apparmor_profile"] = "stale"
		}
	}
	if ver := probeRunscVersion(ctx, d, st.runscPath); ver != "" {
		p.Version = ver
		facts["runsc_version"] = ver
	}
	if kernelOlderThan56(st.release) {
		return st
	}
	if d.SmokeRun != nil {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := d.SmokeRun(sctx, st.runscPath); err != nil {
			st.smokeErr = firstNonEmptyLine(err.Error())
			facts["smoke_ok"] = "false"
			if st.smokeErr != "" {
				facts["smoke_error"] = st.smokeErr
			}
			if st.appArmorRestrict {
				facts["runsc_apparmor"] = "blocked"
			}
		} else {
			st.smokeOK = true
			facts["smoke_ok"] = "true"
			if st.appArmorRestrict {
				facts["runsc_apparmor"] = "allowed"
			}
		}
	}
	return st
}

func diagnoseProbe(p *wall.Probe, st probeState) {
	switch {
	case kernelOlderThan56(st.release):
		p.Reason = fmt.Sprintf("kernel %s is older than Linux 5.6", st.release)
		p.Fix = "upgrade the host kernel to Linux 5.6 or later"
	case st.runscErr != nil || st.runscPath == "":
		p.Reason = fmt.Sprintf("pinned runsc %s is not installed in the artefact store", PinnedRelease)
		if st.runscErr != nil && strings.Contains(st.runscErr.Error(), "SHA-256 mismatch") {
			p.Reason = "pinned runsc in the artefact store failed SHA-256 verification"
		}
		p.Fix = brand.HostBinary + " wall install gvisor"
	case !st.smokeOK && st.hasMaxUserNS && st.maxUserNS == 0:
		p.Reason = "user namespaces are disabled (user.max_user_namespaces = 0)"
		p.Fix = userNSSysctlFix("user.max_user_namespaces", "15000")
	case !st.smokeOK && st.hasUnprivClone && st.unprivClone == 0:
		p.Reason = "unprivileged user namespaces are disabled (kernel.unprivileged_userns_clone = 0)"
		p.Fix = userNSSysctlFix("kernel.unprivileged_userns_clone", "1")
	case !st.smokeOK && st.appArmorRestrict:
		p.Reason = "Ubuntu's AppArmor user-namespace restriction blocks runsc (kernel.apparmor_restrict_unprivileged_userns = 1)"
		if st.profileMatchesRunsc {
			p.Fix = "sudo apparmor_parser -r " + AppArmorProfilePath()
		} else {
			p.Fix = AppArmorFixCmd(st.runscPath)
		}
	case !st.smokeOK:
		if st.smokeErr != "" {
			p.Reason = "runsc smoke test failed: " + st.smokeErr
		} else {
			p.Reason = "runsc smoke test failed"
		}
		p.Fix = "check that unprivileged user namespaces work on this host and run " + brand.HostBinary + " doctor"
	default:
		p.OK = true
	}
}

func kernelOlderThan56(release string) bool {
	maj, minVal, ok := api.ParseKernelMajorMinor(release)
	return ok && (maj < 5 || (maj == 5 && minVal < 6))
}

func probeRunscVersion(ctx context.Context, d ProbeDeps, runscPath string) string {
	if d.RunCmd == nil {
		return ""
	}
	vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := d.RunCmd(vctx, runscPath, "--version")
	if err != nil {
		return ""
	}
	return parseRunscVersion(string(out))
}

// parseRunscVersion extracts "release-20261123.1" from `runsc --version`.
func parseRunscVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "runsc version "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return firstNonEmptyLine(out)
}

// runSmokeTest executes a real runsc smoke container with the exact spec 21.4
// flags and /bin/true, verifying it exits 0 within ctx's 10 s deadline.
func runSmokeTest(ctx context.Context, runscPath string) error {
	tmp, err := os.MkdirTemp("", brand.Name+"-gvisor-smoke-*")
	if err != nil {
		return fmt.Errorf("create smoke temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	trueBin, err := exec.LookPath("true")
	if err != nil || trueBin == "" {
		trueBin = "/bin/true"
	}
	wsHost := filepath.Join(tmp, "work")
	rootDir := filepath.Join(tmp, "runsc")
	bundleDir := filepath.Join(tmp, "bundle")
	for _, d := range []string{wsHost, rootDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("prepare smoke dir: %w", err)
		}
	}
	cid := "smoke-" + strconv.Itoa(os.Getpid())
	spec := Spec{
		SessionID:      cid,
		SessionDir:     tmp,
		WorkspaceHost:  wsHost,
		WorkspaceGuest: "/work/smoke",
		Command:        []string{trueBin},
	}
	if _, err := PrepareBundle(bundleDir, spec); err != nil {
		return err
	}
	args := RunArgs(rootDir, bundleDir, cid, -1)
	if err := ValidateRunArgs(args); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, runscPath, args...).CombinedOutput()
	if err != nil {
		msg := firstNonEmptyLine(string(out))
		if msg != "" {
			return fmt.Errorf("%s (%w)", msg, err)
		}
		return fmt.Errorf("runsc smoke test: %w", err)
	}
	return nil
}

func userNSSysctlFix(key, val string) string {
	conf := "/etc/sysctl.d/50-" + brand.Name + "-userns.conf"
	return fmt.Sprintf("sudo sh -c 'printf \"%s = %s\\n\" > %s && sysctl -p %s'", key, val, conf, conf)
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

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
