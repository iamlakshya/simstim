package host

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// The environment of a walled session's agent (spec 4.2 and 7.1). It comes
// from a fixed base, never from the login environment. The login
// environment's PATH only finds the agent's binary, and the interpreter
// that its #! line names through env, on the trusted side. The guest's
// PATH holds their folders, then the system's.

// guestSystemPath is the end of the guest's PATH, after the folders of the
// agent's binaries (spec 7.1).
const guestSystemPath = "/usr/local/bin:/usr/bin:/bin"

// walledEnv is the agent's environment in a sandbox: the fixed base of spec
// 4.2, the PATH of spec 7.1 for the folders dirs, and the variables that
// every session gets (sessionVars) with the guest's hook socket. It also
// returns the TERM it chose. The order is fixed, so the same session gets
// the same environment.
func walledEnv(a NewArgs, id string, dirs []string, agent agents.Agent, inline bool) (env []string, term string) {
	set, term := walledEnvironmentBase(a, id, dirs, agent, inline)
	keys := make([]string, 0, len(set))
	for k, v := range set {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+set[k])
	}
	return env, term
}

func walledEnvironmentBase(a NewArgs, id string, dirs []string, agent agents.Agent, inline bool) (map[string]string, string) {
	set, term := sessionVars(a, id, GuestHookSocket(), agent, inline)
	for k, v := range map[string]string{"HOME": GuestHome, "USER": GuestUser, "LOGNAME": GuestUser, "LANG": "C.UTF-8", "PATH": guestPath(dirs...)} {
		set[k] = v
	}
	return set, term
}

// guestPath is the guest's PATH: the folders dirs, in order and once each,
// then the system's folders.
func guestPath(dirs ...string) string {
	sys := filepath.SplitList(guestSystemPath)
	var out []string
	for _, d := range dirs {
		if d != "" && !slices.Contains(sys, d) && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return strings.Join(append(out, sys...), string(filepath.ListSeparator))
}

// agentDirs are the folders that the guest's PATH needs for the agent's
// binary bin: its own folder, and the folder of the interpreter that its
// #! line names through env, such as node for an agent that npm installed
// (spec 7.1). path is the login environment's PATH, which finds the
// interpreter as it finds the agent.
func agentDirs(bin, path string) []string {
	dirs := []string{filepath.Dir(bin)}
	if name := envInterpreter(bin); name != "" {
		if p, err := lookPath(name, path); err == nil {
			if resolved, err := filepath.EvalSymlinks(p); err == nil {
				p = resolved
			}
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	return dirs
}

// envInterpreter is the program that the #! line of the file bin names
// through env, as in "#!/usr/bin/env node" or "#!/usr/bin/env -S node
// --flag", or "". It reads the first line only, and at most 256 bytes.
func envInterpreter(bin string) string {
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#!")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 || filepath.Base(fields[0]) != "env" {
		return ""
	}
	for _, f := range fields[1:] {
		switch {
		case f == "-S" || strings.HasPrefix(f, "-"):
			continue
		case strings.Contains(f, "="):
			continue // a variable that env sets
		}
		return f
	}
	return ""
}

// wallInfo is a session's wall, for its row.
func wallInfo(w *WallRecord) *api.WallInfo {
	if w == nil {
		return nil
	}
	return &api.WallInfo{Level: w.Level, Driver: w.Driver, Version: w.Version, Floor: w.Floor, DowngradedFrom: w.DowngradedFrom}
}
