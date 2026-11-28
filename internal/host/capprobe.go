package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall"
	_ "github.com/iamlakshya/simstim/internal/wall/process" // register the bubblewrap process wall driver
)

// CapabilityProbe answers sandbox.probe (isolation spec 14.3).
// It runs every registered wall driver's probe, refreshes StateDir()/walls.json,
// and touches no other file outside artefacts/incoming/.
func CapabilityProbe(releaseURL string) api.CapProbe {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	p := api.CapProbe{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Kernel:          probeKernel(),
		Distro:          readOSPrettyName("/etc/os-release"),
		UID:             os.Getuid(),
		IOURingDisabled: -1,
		MemTotal:        MemTotal(),
		MemForSessions:  DefaultMemoryMax(),
		Walls:           []api.WallProbe{},
	}
	u, err := user.Current()
	if err == nil && u != nil {
		p.User = u.Username
	}

	p.Systemd = probeSystemdVersion(ctx)
	p.UserManager = SystemdUser()
	if p.UserManager {
		p.Linger = lingerOn()
	}
	p.CgroupV2, p.Delegated = probeCgroupDelegation()
	p.KVM = probeKVM(p.UID, u)
	p.CPUVirt = readCPUInfoVirt(filepath.Join(procRoot, "cpuinfo"))
	p.Virt = probeVirt(ctx)
	p.IOURingDisabled = readProcInt(filepath.Join(procRoot, "sys/kernel/io_uring_disabled"), -1)
	p.VsockBlocked = exists("/etc/modprobe.d/50-" + brand.Name + "-vsock.conf")
	p.Subuid = hasSubIDFiles("/etc/subuid", "/etc/subgid", p.User, p.UID)
	_, err = exec.LookPath("setfacl")
	p.HasSetfacl = err == nil
	_, err1 := exec.LookPath("newuidmap")
	_, err2 := exec.LookPath("newgidmap")
	p.HasNewuidmap = err1 == nil && err2 == nil
	p.Sudo, p.SudoPasswordless = probeSudo(ctx)

	dataDir := paths.DataDir()
	diskDir := existingAncestor(dataDir)
	p.DiskFree = diskFree(diskDir)
	p.DiskFS = probeDiskFS(diskDir)
	p.Reflink = probeReflink(paths.ArtefactsIncomingDir())

	p.RootSteps = readAppliedRootSteps(p.User)
	p.Runtimes = listInstalledRuntimes(filepath.Join(paths.ArtefactsDir(), "runtime"))
	p.Started = readStartedCount(filepath.Join(paths.StateDir(), "sandboxes-started"))
	if releaseURL != "" {
		ok := probeReleaseReachable(ctx, releaseURL)
		p.ReleaseReachable = &ok
	}

	probes := wall.ProbeAll(ctx)
	_ = wall.WriteCache(paths.WallProbes(), BootID(), version.Version, probes)
	p.Walls = wall.ToAPIProbes(probes)
	applyProcessFacts(&p, probes)
	return p
}

func applyProcessFacts(p *api.CapProbe, probes []wall.Probe) {
	for _, wp := range probes {
		if wp.Level != wall.Process {
			continue
		}
		if v := wp.Facts["bwrap"]; v != "" {
			p.Bwrap = true
			p.BwrapPath = v
		}
		if v := wp.Facts["bwrap_version"]; v != "" {
			p.BwrapVersion = v
		} else if wp.Version != "" {
			p.BwrapVersion = wp.Version
		}
		p.BwrapAppArmor = wp.Facts["bwrap_apparmor"]
		if abi, err := strconv.Atoi(wp.Facts["landlock_abi"]); err == nil {
			p.LandlockABI = abi
		}
		p.Seccomp = wp.Facts["seccomp"] == "true"
		p.UserNS = wp.Facts["userns"] == "true"
		p.UserNSRestricted = wp.Facts["apparmor_userns"] == "true"
		return
	}
}

func probeKernel() string {
	b, err := exec.CommandContext(context.Background(), "uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readOSPrettyName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseOSReleasePrettyName(b)
}

func parseOSReleasePrettyName(b []byte) string {
	var name, versionID string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
		if v, ok := strings.CutPrefix(line, "NAME="); ok {
			name = strings.Trim(v, `"'`)
		}
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			versionID = strings.Trim(v, `"'`)
		}
	}
	if name != "" && versionID != "" {
		return name + " " + versionID
	}
	return name
}

func probeSystemdVersion(ctx context.Context) int {
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(tctx, "systemctl", "--version").Output()
	if err != nil {
		return 0
	}
	return parseSystemdVersion(out)
}

func parseSystemdVersion(out []byte) int {
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "systemd") {
		n, err := strconv.Atoi(fields[1])
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func probeCgroupDelegation() (v2 bool, delegated []string) {
	if !exists(filepath.Join(cgroupRoot, "cgroup.controllers")) {
		return false, nil
	}
	b, err := os.ReadFile(filepath.Join(userManagerCgroup(), "cgroup.controllers"))
	if err != nil {
		return true, nil
	}
	return true, parseCgroupControllers(b)
}

func parseCgroupControllers(b []byte) []string {
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func readCPUInfoVirt(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseCPUInfoVirt(b)
}

func parseCPUInfoVirt(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if key != "flags" && key != "features" {
			continue
		}
		for _, f := range strings.Fields(v) {
			if f == "vmx" || f == "svm" {
				return f
			}
		}
	}
	return ""
}

func probeVirt(ctx context.Context) string {
	p, err := exec.LookPath("systemd-detect-virt")
	if err != nil {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(tctx, p).Output()
	v := strings.TrimSpace(string(out))
	if err != nil && (v == "" || v == "none") {
		return ""
	}
	if v == "none" {
		return ""
	}
	return v
}

func readProcInt(path string, fallback int) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fallback
	}
	return n
}

func hasSubIDFiles(subuidPath, subgidPath, username string, uid int) bool {
	if username == "" {
		return false
	}
	b1, err1 := os.ReadFile(subuidPath)
	b2, err2 := os.ReadFile(subgidPath)
	if err1 != nil || err2 != nil {
		return false
	}
	return parseSubID(b1, username, uid) && parseSubID(b2, username, uid)
}

func parseSubID(b []byte, username string, uid int) bool {
	uidStr := strconv.Itoa(uid)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}
		if parts[0] != username && parts[0] != uidStr {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if err == nil && count > 0 {
			return true
		}
	}
	return false
}

func probeSudo(ctx context.Context) (hasSudo, passwordless bool) {
	if !exists("/usr/bin/sudo") {
		return false, false
	}
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := exec.CommandContext(tctx, "/usr/bin/sudo", "-n", "true").Run()
	return true, err == nil
}

func existingAncestor(path string) string {
	cur := path
	for {
		if exists(cur) {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "/"
		}
		cur = parent
	}
}

func readAppliedRootSteps(username string) []string {
	if username == "" || strings.ContainsAny(username, "/\\") {
		return nil
	}
	p := filepath.Join("/var/lib", brand.Name, "root-steps", username, "applied.json")
	b, ok := readRootOwnedFile(p)
	if !ok {
		return nil
	}
	var doc struct {
		Steps []string `json:"steps"`
	}
	if json.Unmarshal(b, &doc) != nil || len(doc.Steps) == 0 {
		return nil
	}
	return doc.Steps
}

func listInstalledRuntimes(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func readStartedCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func probeReleaseReachable(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < http.StatusBadRequest
}

// parsePOSIXACLAccess reports whether a Linux system.posix_acl_access xattr
// grants uid read and write access, taking any ACL_MASK entry into account.
func parsePOSIXACLAccess(xattr []byte, uid uint32) bool {
	const (
		aclVersion = 2
		aclUser    = 0x02
		aclMask    = 0x10
		aclRead    = 0x04
		aclWrite   = 0x02
		aclRW      = aclRead | aclWrite
	)
	if len(xattr) < 4 || (len(xattr)-4)%8 != 0 {
		return false
	}
	if binary.LittleEndian.Uint32(xattr[:4]) != aclVersion {
		return false
	}
	var userPerm uint16
	foundUser := false
	maskPerm := uint16(aclRW)
	for off := 4; off+8 <= len(xattr); off += 8 {
		tag := binary.LittleEndian.Uint16(xattr[off : off+2])
		perm := binary.LittleEndian.Uint16(xattr[off+2 : off+4])
		id := binary.LittleEndian.Uint32(xattr[off+4 : off+8])
		switch tag {
		case aclUser:
			if id == uid {
				userPerm = perm
				foundUser = true
			}
		case aclMask:
			maskPerm = perm
		}
	}
	return foundUser && (userPerm&maskPerm&aclRW) == aclRW
}

func parseProcStatusGroups(b []byte) []int {
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Groups:"); ok {
			fields := strings.Fields(rest)
			out := make([]int, 0, len(fields))
			for _, f := range fields {
				if g, err := strconv.Atoi(f); err == nil {
					out = append(out, g)
				}
			}
			return out
		}
	}
	return nil
}
