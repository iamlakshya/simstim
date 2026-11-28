package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultGuestCgroupRoot = "/sys/fs/cgroup"
	defaultAgentCgroupDir  = "/sys/fs/cgroup/agent"
	defaultAgentsBinDir    = "/opt/agents/bin"
	agentOOMScoreAdj       = "500"
)

// placePIDInAgentCgroup moves pid into the guest agent cgroup and raises its
// oom_score_adj so OOM kills hit agent/exec processes before PID 1 or holders.
func placePIDInAgentCgroup(cgroupDir string, pid int) {
	if pid <= 1 {
		return
	}
	if cgroupDir != "" {
		//nolint:gosec // G306: cgroup control files use 0644
		_ = os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}
	//nolint:gosec // G306: procfs oom_score_adj is a kernel control file
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", pid), []byte(agentOOMScoreAdj), 0o644)
}

// ensureAgentsPath prepends binDir (default /opt/agents/bin) to PATH in env
// when binDir exists as a directory (isolation spec 20.4).
func ensureAgentsPath(env []string, binDir string) []string {
	if binDir == "" {
		binDir = defaultAgentsBinDir
	}
	fi, err := os.Stat(binDir)
	if err != nil || !fi.IsDir() {
		return env
	}
	cur := envLookup(env, "PATH")
	if cur == "" {
		return withEnvVar(env, "PATH", binDir+":/usr/local/bin:/usr/bin:/bin")
	}
	parts := strings.Split(cur, ":")
	if parts[0] == binDir {
		return env
	}
	out := make([]string, 0, len(parts)+1)
	out = append(out, binDir)
	for _, p := range parts {
		if p != "" && p != binDir {
			out = append(out, p)
		}
	}
	return withEnvVar(env, "PATH", strings.Join(out, ":"))
}
