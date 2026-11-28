package host

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
)

// mapAgentMounts computes read-only mounts under /opt/agents/ for an agent
// binary (and any shebang interpreter or agent InstallDirs) that live outside
// the read-only system trees (/usr, /bin, /sbin, /lib, /lib64), never exposing
// the host user's home folder path.
func mapAgentMounts(resolvedBin, searchPath string, agent agents.Agent) (guestArg0 string, mounts []WallMount, pathDirs []string) {
	realBin, err := filepath.EvalSymlinks(resolvedBin)
	if err != nil {
		realBin = resolvedBin
	}
	// Resolve the home folder as the binary is resolved, so a home behind a
	// symlink (such as /home -> /var/home) still matches the binary's path.
	home, _ := os.UserHomeDir()
	if real, rerr := filepath.EvalSymlinks(home); home != "" && rerr == nil {
		home = real
	}
	cleanHome := filepath.Clean(home)
	mounted := map[string]string{} // hostSrc -> guestDest
	idx := 0
	addMount := func(src, dest string) string {
		src = filepath.Clean(src)
		if existing, ok := mounted[src]; ok {
			return existing
		}
		mounted[src] = dest
		mounts = append(mounts, WallMount{Source: src, Dest: dest, ReadOnly: true})
		return dest
	}

	if home != "" {
		mountInstallDirs(cleanHome, agent.InstallDirs, &idx, addMount)
	}

	guestArg0 = realBin
	if !isSystemPath(realBin) {
		if mapped, ok := mapInsideExisting(realBin, mounted); ok {
			guestArg0 = mapped
			pathDirs = appendUnique(pathDirs, filepath.Dir(mapped))
		} else {
			dest, binDir := mountNonSystemBinary(realBin, cleanHome, &idx, addMount)
			guestArg0 = dest
			pathDirs = appendUnique(pathDirs, binDir)
		}
		// If the command was found via a symlink with a different basename
		// (for example ~/.local/bin/claude -> ~/.local/share/claude/versions/2.1.332),
		// also bind the resolved binary under its command name on guest PATH so
		// resume commands ("claude --resume ...") and subshells find it by name.
		origBase := filepath.Base(resolvedBin)
		if origBase != "" && origBase != "." && origBase != filepath.Base(guestArg0) {
			aliasDir := "/opt/agents/bin"
			aliasPath := aliasDir + "/" + origBase
			mounts = append(mounts, WallMount{Source: filepath.Clean(realBin), Dest: aliasPath, ReadOnly: true})
			guestArg0 = aliasPath
			pathDirs = appendUnique(pathDirs, aliasDir)
		}
	}

	// If the binary is a script starting with #!/usr/bin/env <name> or #!<path>,
	// ensure a non-system interpreter (for example nvm node) is also mounted RO.
	if interp := shebangInterpreter(realBin, searchPath); interp != "" && !isSystemPath(interp) {
		if realInterp, rerr := filepath.EvalSymlinks(interp); rerr == nil {
			interp = realInterp
		}
		if !isSystemPath(interp) {
			if mapped, ok := mapInsideExisting(interp, mounted); ok {
				pathDirs = appendUnique(pathDirs, filepath.Dir(mapped))
			} else {
				_, interpDir := mountNonSystemBinary(interp, cleanHome, &idx, addMount)
				pathDirs = appendUnique(pathDirs, interpDir)
			}
		}
	}
	return guestArg0, mounts, pathDirs
}

// mountInstallDirs mounts the agent's install folders that exist under home
// read-only under /opt/agents/.
func mountInstallDirs(home string, rels []string, idx *int, addMount func(src, dest string) string) {
	for _, rel := range rels {
		hostDir := filepath.Clean(filepath.Join(home, rel))
		if real, err := filepath.EvalSymlinks(hostDir); err == nil {
			hostDir = real
		}
		// An install folder that is, or links to, the home folder would put
		// all of home in the sandbox.
		if hostDir == home {
			continue
		}
		if fi, err := os.Stat(hostDir); err == nil && fi.IsDir() {
			addMount(hostDir, fmt.Sprintf("/opt/agents/install-%d", *idx))
			*idx++
		}
	}
}

func mapInsideExisting(path string, mounted map[string]string) (string, bool) {
	clean := filepath.Clean(path)
	for hostSrc, guestDest := range mounted {
		if rel, err := filepath.Rel(hostSrc, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return guestDest, true
			}
			return guestDest + "/" + filepath.ToSlash(rel), true
		}
	}
	return "", false
}

func mountNonSystemBinary(realBin, cleanHome string, idx *int, addMount func(src, dest string) string) (guestBin, guestDir string) {
	dir := filepath.Dir(realBin)
	parent := filepath.Dir(dir)
	// When the binary sits in a dedicated toolchain's bin/ folder (such as
	// ~/.nvm/versions/node/v22/bin/node), mount the toolchain root so sibling
	// lib/ files work, while never mounting $HOME, ~/.local, /, /tmp or /var.
	if filepath.Base(dir) == "bin" && safeToolchainRoot(parent, cleanHome) {
		rootDest := fmt.Sprintf("/opt/agents/tool-%d", *idx)
		*idx++
		addMount(parent, rootDest)
		guestDir = rootDest + "/bin"
		return guestDir + "/" + filepath.Base(realBin), guestDir
	}
	// Otherwise mount only the single executable file so no sibling files from
	// ~/.local/bin or /tmp enter the sandbox.
	guestDir = fmt.Sprintf("/opt/agents/bin-%d", *idx)
	*idx++
	guestBin = guestDir + "/" + filepath.Base(realBin)
	addMount(realBin, guestBin)
	return guestBin, guestDir
}

func safeToolchainRoot(dir, cleanHome string) bool {
	c := filepath.Clean(dir)
	switch c {
	case "/", "/tmp", "/var", "/usr", "/opt", "/home", cleanHome:
		return false
	}
	if cleanHome != "" && (c == filepath.Join(cleanHome, ".local") || c == filepath.Join(cleanHome, ".config")) {
		return false
	}
	return true
}

func isSystemPath(p string) bool {
	c := filepath.Clean(p)
	for _, prefix := range []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/"} {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func shebangInterpreter(path, searchPath string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#!")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if filepath.Base(fields[0]) == "env" && len(fields) >= 2 {
		name := fields[1]
		if name == "-S" && len(fields) >= 3 {
			name = fields[2]
		}
		if p, lerr := lookPath(name, searchPath); lerr == nil {
			return p
		}
		return ""
	}
	return fields[0]
}
