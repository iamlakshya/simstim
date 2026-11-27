package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

const capabilityTimeout = 20 * time.Second

// capSource keeps file and command reads replaceable in tests on any OS.
type capSource struct {
	root                           string
	os, arch, username             string
	uid                            int
	accountGroups, processGroups   []string
	run                            func(context.Context, string, ...string) ([]byte, error)
	lookup                         func(string) bool
	dataDir, incomingDir, stateDir string
	wallProbes                     func(context.Context) []wall.Probe
	disk                           func(string, string) (int64, string, bool)
}

func (s capSource) path(name string) string {
	return filepath.Join(s.root, strings.TrimPrefix(name, "/"))
}
func (s capSource) read(name string) []byte {
	b, _ := readProbeFile(s.path(name), 4<<20)
	return b
}

func liveCapSource() capSource {
	s := capSource{root: "/", os: runtime.GOOS, arch: runtime.GOARCH, uid: os.Getuid(),
		run: probeCommand, lookup: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		dataDir: paths.DataDir(), stateDir: paths.StateDir(), incomingDir: filepath.Join(paths.DataDir(), "artefacts", "incoming")}
	if u, err := user.Current(); err == nil {
		s.username = u.Username
		s.accountGroups, _ = u.GroupIds()
		if !slices.Contains(s.accountGroups, u.Gid) {
			s.accountGroups = append(s.accountGroups, u.Gid)
		}
	}
	if groups, err := os.Getgroups(); err == nil {
		groups = append(groups, os.Getgid())
		for _, g := range groups {
			s.processGroups = append(s.processGroups, strconv.Itoa(g))
		}
	}
	return s
}

func capabilityProbe(ctx context.Context, releaseURL string, s capSource) (api.CapProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, capabilityTimeout)
	defer cancel()
	p := api.CapProbe{OS: s.os, Arch: s.arch, User: s.username, UID: s.uid, Delegated: []string{}, Walls: []api.WallProbe{}}
	if err := ctx.Err(); err != nil {
		return p, fmt.Errorf("probe sandbox capabilities: %w", err)
	}
	p.Distro = probeOSRelease(s.read("/etc/os-release"))
	p.Kernel = strings.TrimSpace(string(s.read("/proc/sys/kernel/osrelease")))
	if p.Kernel == "" {
		p.Kernel = strings.TrimSpace(capOutput(ctx, s, "uname", "-r"))
	}
	p.CPUVirt = probeCPUVirt(s.read("/proc/cpuinfo"), s.arch)
	p.MemTotal = probeMemTotal(s.read("/proc/meminfo"))
	if p.MemTotal == 0 && s.os != "linux" {
		p.MemTotal = MemTotal()
	}
	p.MemForSessions = memoryMaxFor(p.MemTotal)
	p.HasSetfacl = s.lookup("setfacl")
	p.HasNewuidmap = s.lookup("newuidmap") && s.lookup("newgidmap")
	p.Subuid = probeSubID(s.read("/etc/subuid"), s.username, s.uid) && probeSubID(s.read("/etc/subgid"), s.username, s.uid)
	p.Sudo = regularExecutable(s.path("/usr/bin/sudo"))
	if p.Sudo {
		_, err := s.run(ctx, "/usr/bin/sudo", "-n", "true")
		p.SudoPasswordless = err == nil
	}
	p.Virt = strings.TrimSpace(capOutput(ctx, s, "systemd-detect-virt"))
	started, _ := readProbeFile(filepath.Join(s.stateDir, "sandboxes-started"), 32)
	p.Started, _ = strconv.Atoi(strings.TrimSpace(string(started)))
	if p.Started < 0 {
		p.Started = 0
	}
	readPlatformCapabilities(ctx, s, &p)
	disk := s.disk
	if disk == nil {
		disk = probeDisk
	}
	p.DiskFree, p.DiskFS, p.Reflink = disk(s.dataDir, s.incomingDir)
	p.RootSteps = probeRootSteps(s)
	entries, _ := os.ReadDir(filepath.Join(s.dataDir, "artefacts", "runtime"))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			p.Runtimes = append(p.Runtimes, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	readWalls := s.wallProbes
	if readWalls == nil {
		readWalls = probeWalls
	}
	probes := readWalls(ctx)
	for _, probe := range probes {
		p.Walls = append(p.Walls, api.WallProbe{CanPrepare: probe.CanPrepare, Driver: probe.Driver, Level: probe.Level.String(), OK: probe.OK, Reason: probe.Reason, Fix: probe.Fix, Version: probe.Version, Facts: probe.Facts})
		if probe.Driver == "process" {
			p.UserNS = probe.Facts["userns"] == "true"
			p.LandlockABI, _ = strconv.Atoi(probe.Facts["landlock_abi"])
			p.Seccomp = p.Seccomp || probe.Facts["seccomp"] == "true"
		}
	}
	if releaseURL != "" {
		reachable, err := probeRelease(ctx, releaseURL)
		if err != nil {
			return p, err
		}
		p.ReleaseReachable = &reachable
	}
	if err := ctx.Err(); err != nil {
		return p, fmt.Errorf("probe sandbox capabilities: %w", err)
	}
	return p, nil
}

func probeCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return b, fmt.Errorf("probe %s: %w", name, err)
	}
	return b, nil
}

func capOutput(ctx context.Context, s capSource, name string, args ...string) string {
	b, _ := s.run(ctx, name, args...)
	return string(b)
}

func regularExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

func readProbeFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || limit <= 0 || limit > 512<<20 {
		return nil, fmt.Errorf("invalid bounded capability file read")
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, fmt.Errorf("anchor capability file: %w", err)
	}
	defer root.Close()
	f, err := root.Open(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, fmt.Errorf("open capability file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read capability file: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("capability file exceeds %d bytes", limit)
	}
	return b, nil
}

func probeRelease(ctx context.Context, raw string) (bool, error) {
	c := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.User != nil {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	return probeReleaseWithClient(ctx, raw, &c)
}

func probeReleaseWithClient(ctx context.Context, raw string, c *http.Client) (bool, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false, UserError("bad_request", "release_url must be an HTTPS URL without account details", "Use an HTTPS release URL, or leave release_url empty.")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return false, fmt.Errorf("make release probe: %w", err)
	}
	res, err := c.Do(req)
	if err != nil {
		return false, nil
	}
	defer res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300, nil
}

func probeRootSteps(s capSource) []string {
	if s.username == "" || s.username == "." || s.username == ".." || strings.ContainsAny(s.username, "/\\") {
		return nil
	}
	path := s.path(filepath.Join("/var/lib", brand.Name, "root-steps", s.username, "applied.json"))
	if !trustedRootFile(path) {
		return nil
	}
	b, err := readProbeFile(path, 1<<20)
	if err != nil {
		return nil
	}
	var steps []string
	if json.Unmarshal(b, &steps) != nil {
		var v struct {
			Steps []string `json:"steps"`
		}
		if json.Unmarshal(b, &v) != nil {
			return nil
		}
		steps = v.Steps
	}
	slices.Sort(steps)
	return slices.Compact(steps)
}

func probeWalls(ctx context.Context) []wall.Probe {
	var probes []wall.Probe
	processRegistered := false
	for _, driver := range wall.Drivers() {
		probes = append(probes, driver.Probe(ctx))
		processRegistered = processRegistered || driver.Name() == "process"
	}
	if !processRegistered {
		probes = append(probes, process.Probe(ctx))
	}
	return probes
}
