package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
)

const (
	// GuestHolderUID is the dedicated unprivileged UID for the guest holder
	// inside a microvm (isolation spec 17.10).
	GuestHolderUID = 900
	// GuestHolderGID is the primary GID for the guest holder inside a microvm.
	GuestHolderGID = 900
	// GuestAgentUID is the dedicated UID for the agent and guest shells inside
	// a microvm (isolation spec 7.1, 17.10).
	GuestAgentUID = 1000
	// GuestAgentGID is the primary GID for the agent inside a microvm.
	GuestAgentGID = 1000
	// DefaultDockerGID is allocated for the docker group when --docker is
	// enabled on an image that does not already define a docker group.
	DefaultDockerGID = 999
)

type vmBootConfig struct {
	SelfTest   bool
	KeepSystem bool
	Sudo       bool
	Docker     bool
}

func parseMicroVMFlags(args []string) vmBootConfig {
	var cfg vmBootConfig
	for _, a := range args {
		switch a {
		case "--self-test":
			cfg.SelfTest = true
		case "--keep-system":
			cfg.KeepSystem = true
		case "--sudo":
			cfg.Sudo = true
		case "--docker":
			cfg.Docker = true
		}
	}
	return cfg
}

// allAgentStateDirs returns the sorted, deduplicated union of clean relative
// StateDirs across the built-in agent table (isolation spec 7.1).
func allAgentStateDirs() []string {
	seen := map[string]struct{}{}
	var dirs []string
	for _, a := range agents.Table {
		for _, raw := range a.StateDirs {
			rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
			if rel == "" || rel == "." || strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			if _, ok := seen[rel]; ok {
				continue
			}
			seen[rel] = struct{}{}
			dirs = append(dirs, rel)
		}
	}
	sort.Strings(dirs)
	return dirs
}

// configureGuestEtc prepares /etc/passwd, /etc/group, /etc/hostname,
// /etc/hosts, /etc/resolv.conf, and /etc/sudoers.d/agent inside rootDir for a
// microvm boot. It returns the docker GID when cfg.Docker is true.
func configureGuestEtc(rootDir string, cfg vmBootConfig) (dockerGID int, err error) {
	etcDir := filepath.Join(rootDir, "etc")
	//nolint:gosec // G301: /etc must be world-readable/traversable
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", etcDir, err)
	}

	shell := "/bin/sh"
	if fi, serr := os.Stat(filepath.Join(rootDir, "bin", "bash")); serr == nil && !fi.IsDir() {
		shell = "/bin/bash"
	}

	passwdPath := filepath.Join(etcDir, "passwd")
	passwdRaw, _ := os.ReadFile(passwdPath)
	passwdOut := ensurePasswdEntries(string(passwdRaw), shell)
	//nolint:gosec // G306: /etc/passwd must be mode 0644
	if err := os.WriteFile(passwdPath, []byte(passwdOut), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", passwdPath, err)
	}

	groupPath := filepath.Join(etcDir, "group")
	groupRaw, _ := os.ReadFile(groupPath)
	groupOut, dockerGID := ensureGroupEntries(string(groupRaw), cfg.Docker)
	//nolint:gosec // G306: /etc/group must be mode 0644
	if err := os.WriteFile(groupPath, []byte(groupOut), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", groupPath, err)
	}

	//nolint:gosec // G306: standard /etc files must be world-readable (0644)
	if err := os.WriteFile(filepath.Join(etcDir, "hostname"), []byte("sandbox\n"), 0o644); err != nil {
		return 0, fmt.Errorf("write /etc/hostname: %w", err)
	}
	//nolint:gosec // G306: /etc/hosts must be mode 0644
	if err := os.WriteFile(filepath.Join(etcDir, "hosts"), []byte("127.0.0.1 localhost sandbox\n::1 localhost\n"), 0o644); err != nil {
		return 0, fmt.Errorf("write /etc/hosts: %w", err)
	}
	// Remove a dangling resolv.conf symlink before writing our synthetic stub resolver.
	resolvPath := filepath.Join(etcDir, "resolv.conf")
	_ = os.Remove(resolvPath)
	//nolint:gosec // G306: /etc/resolv.conf must be mode 0644
	if err := os.WriteFile(resolvPath, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		return 0, fmt.Errorf("write /etc/resolv.conf: %w", err)
	}

	if err := configureGuestSudoers(etcDir, cfg.Sudo); err != nil {
		return 0, err
	}
	return dockerGID, nil
}

func configureGuestSudoers(etcDir string, allowSudo bool) error {
	sudoersDir := filepath.Join(etcDir, "sudoers.d")
	agentFile := filepath.Join(sudoersDir, "agent")
	if !allowSudo {
		if err := os.Remove(agentFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", agentFile, err)
		}
		return nil
	}
	if err := os.MkdirAll(sudoersDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", sudoersDir, err)
	}
	const rule = "agent ALL=(ALL:ALL) NOPASSWD: ALL\n"
	//nolint:gosec // G306: sudoers drop-in requires 0440 group-readable permissions
	if err := os.WriteFile(agentFile, []byte(rule), 0o440); err != nil {
		return fmt.Errorf("write %s: %w", agentFile, err)
	}
	//nolint:gosec // G302: sudoers drop-in requires 0440 permissions
	_ = os.Chmod(agentFile, 0o440)

	sudoersPath := filepath.Join(etcDir, "sudoers")
	if raw, err := os.ReadFile(sudoersPath); err == nil {
		s := string(raw)
		if !strings.Contains(s, "includedir /etc/sudoers.d") {
			if len(s) > 0 && !strings.HasSuffix(s, "\n") {
				s += "\n"
			}
			s += "@includedir /etc/sudoers.d\n"
			_ = os.Chmod(sudoersPath, 0o600)
			//nolint:gosec // G306: /etc/sudoers requires 0440 permissions
			if werr := os.WriteFile(sudoersPath, []byte(s), 0o440); werr != nil {
				return fmt.Errorf("update %s: %w", sudoersPath, werr)
			}
			//nolint:gosec // G302: /etc/sudoers requires 0440 permissions
			_ = os.Chmod(sudoersPath, 0o440)
		}
	}
	return nil
}

func ensurePasswdEntries(content, agentShell string) string {
	if agentShell == "" {
		agentShell = "/bin/sh"
	}
	var kept []string
	hasRoot := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		name, uid := fields[0], fields[2]
		if name == "holder" || name == "agent" || uid == strconv.Itoa(GuestHolderUID) || uid == strconv.Itoa(GuestAgentUID) {
			continue
		}
		if name == "root" {
			hasRoot = true
		}
		kept = append(kept, line)
	}
	if !hasRoot {
		kept = append([]string{"root:x:0:0:root:/root:/bin/sh"}, kept...)
	}
	kept = append(kept,
		fmt.Sprintf("holder:x:%d:%d:holder:/nonexistent:/usr/sbin/nologin", GuestHolderUID, GuestHolderGID),
		fmt.Sprintf("agent:x:%d:%d:agent:/home/agent:%s", GuestAgentUID, GuestAgentGID, agentShell),
	)
	return strings.Join(kept, "\n") + "\n"
}

func ensureGroupEntries(content string, docker bool) (string, int) {
	kept, usedGIDs, hasRoot, hasTTY, dockerGID, dockerMembers := scanGroupEntries(content, docker)
	if !hasRoot {
		kept = append([]string{"root:x:0:"}, kept...)
	}
	if !hasTTY {
		kept = append(kept, "tty:x:5:")
	}
	kept = append(kept,
		fmt.Sprintf("holder:x:%d:", GuestHolderGID),
		fmt.Sprintf("agent:x:%d:", GuestAgentGID),
	)
	if docker {
		dockerGID = pickDockerGID(dockerGID, usedGIDs)
		if !slices.Contains(dockerMembers, "agent") {
			dockerMembers = append(dockerMembers, "agent")
		}
		kept = append(kept, fmt.Sprintf("docker:x:%d:%s", dockerGID, strings.Join(dockerMembers, ",")))
	}
	return strings.Join(kept, "\n") + "\n", dockerGID
}

func scanGroupEntries(content string, docker bool) (kept []string, usedGIDs map[int]bool, hasRoot, hasTTY bool, dockerGID int, dockerMembers []string) {
	usedGIDs = map[int]bool{0: true, 5: true, GuestHolderGID: true, GuestAgentGID: true}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Split(line, ":")
		if line == "" || len(fields) < 3 {
			continue
		}
		name := fields[0]
		gid, err := strconv.Atoi(fields[2])
		if name == "holder" || name == "agent" || gid == GuestHolderGID || gid == GuestAgentGID {
			continue
		}
		if name == "docker" && docker {
			if err == nil && gid > 0 {
				dockerGID = gid
			}
			if len(fields) >= 4 {
				dockerMembers = appendUniqueMembers(dockerMembers, fields[3])
			}
			continue
		}
		if err == nil {
			usedGIDs[gid] = true
		}
		hasRoot = hasRoot || name == "root"
		hasTTY = hasTTY || name == "tty"
		kept = append(kept, line)
	}
	return kept, usedGIDs, hasRoot, hasTTY, dockerGID, dockerMembers
}

func appendUniqueMembers(dst []string, csv string) []string {
	for _, m := range strings.Split(csv, ",") {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(dst, m) {
			dst = append(dst, m)
		}
	}
	return dst
}

func pickDockerGID(existing int, used map[int]bool) int {
	if existing > 0 {
		return existing
	}
	gid := DefaultDockerGID
	for used[gid] {
		gid--
	}
	return gid
}

func (r *runner) agentGroups() []uint32 {
	groups := []uint32{GuestAgentGID}
	if r.opts.Docker && r.opts.DockerGID > 0 && r.opts.DockerGID != GuestAgentGID {
		//nolint:gosec // G115: validated positive Linux GID
		groups = append(groups, uint32(r.opts.DockerGID))
	}
	return groups
}

func (r *runner) holderPaths(sessionID string, first bool) (sockPath, hookPath, manifestPath, logPath string, err error) {
	sockName, hookName := "holder-"+sessionID+".sock", "hook-"+sessionID+".sock"
	if first {
		sockName, hookName = "holder.sock", "hook.sock"
	}
	if !r.opts.MicroVM {
		return filepath.Join(r.runDir, sockName),
			filepath.Join(r.runDir, hookName),
			filepath.Join(r.runDir, "manifest-"+sessionID+".json"),
			filepath.Join(r.runDir, "holder-"+sessionID+".log"),
			nil
	}
	holderDir := filepath.Join(r.runDir, "h-"+sessionID)
	if err := chownMicroVMDir(holderDir, GuestHolderUID, GuestHolderGID, 0o700); err != nil {
		return "", "", "", "", err
	}
	hookDir := filepath.Join(r.runDir, "hooks")
	if err := chownMicroVMDir(hookDir, GuestHolderUID, GuestAgentGID, 0o750); err != nil {
		return "", "", "", "", err
	}
	return filepath.Join(holderDir, sockName),
		filepath.Join(hookDir, hookName),
		filepath.Join(holderDir, "manifest-"+sessionID+".json"),
		filepath.Join(holderDir, "holder-"+sessionID+".log"),
		nil
}

func (r *runner) agentCgroupDir() string {
	if r.opts.AgentCgroupDir != "" {
		return r.opts.AgentCgroupDir
	}
	if r.opts.MicroVM {
		return defaultAgentCgroupDir
	}
	return ""
}

func (r *runner) agentsBinDir() string {
	if r.opts.AgentsBinDir != "" {
		return r.opts.AgentsBinDir
	}
	return defaultAgentsBinDir
}

func (r *runner) emitMemClaim() {
	sample, ok := readGuestMem("", r.agentCgroupDir())
	if !ok {
		return
	}
	r.sendEvent(Event{
		Type:         "mem",
		MemUsed:      sample.Used,
		MemTotal:     sample.Total,
		MemAvailable: sample.Available,
		OOMKills:     sample.OOMKills,
	})
}
