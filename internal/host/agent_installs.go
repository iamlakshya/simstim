package host

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/gitsource"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// resolveAgentInstalls pins only the selected executable/interpreter program
// directories and the canonical declared install roots that contain them.
// Login PATH locates programs; it never grants the guest the host home.
func resolveAgentInstalls(binary, loginPath, home string) ([]wall.Mount, []string, error) {
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, nil, UserError("wall_files", "the selected executable cannot be resolved", "Reinstall the selected program before starting the sandbox.")
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, nil, UserError("wall_files", "the host home cannot be resolved", "Check the host account before starting the sandbox.")
	}
	directories := agentDirs(resolved, loginPath)
	var declared, private []string
	private = append(private, paths.StateDir(), paths.ConfigDir(), paths.RuntimeDir())
	for _, relative := range []string{".ssh", ".aws", ".config", ".gnupg", ".kube", ".docker", "Library/Keychains"} {
		private = append(private, filepath.Join(home, relative))
	}
	for _, agent := range agents.Table {
		for _, relative := range agent.InstallDirs {
			if !fs.ValidPath(relative) || relative == "." {
				return nil, nil, UserError("wall_files", "the canonical install folder is invalid", "Update the host helper.")
			}
			declared = append(declared, filepath.Join(home, filepath.FromSlash(relative)))
		}
		for _, relative := range agent.StateDirs {
			if relative != "" && fs.ValidPath(relative) {
				private = append(private, filepath.Join(home, filepath.FromSlash(relative)))
			}
		}
		if agent.Setup != nil {
			for _, name := range agent.Setup.LoginFiles {
				if strings.HasPrefix(name, "~/") {
					private = append(private, filepath.Dir(filepath.Join(home, strings.TrimPrefix(name, "~/"))))
				}
			}
		}
	}
	var mounts []wall.Mount
	for _, directory := range directories {
		if systemProgramDirectory(directory) {
			continue
		}
		chosen := directory
		for _, root := range declared {
			if gitsource.Beneath(directory, root) && (chosen == directory || len(root) > len(chosen)) {
				chosen = root
			}
		}
		// npm's package root is a program installation, not its whole global
		// prefix or a caller's repository. Relative package imports stay valid.
		if chosen == directory {
			for parent := directory; parent != filepath.Dir(parent) && parent != home; parent = filepath.Dir(parent) {
				if !strings.Contains(parent, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) {
					continue
				}
				if info, err := os.Lstat(filepath.Join(parent, "package.json")); err == nil && info.Mode().IsRegular() {
					chosen = parent
					break
				}
			}
		}
		if chosen == home || chosen == string(filepath.Separator) || !filepath.IsAbs(chosen) || filepath.Clean(chosen) != chosen {
			return nil, nil, UserError("wall_files", "the program would require exposing the host home", "Install it in a separate program folder before starting the sandbox.")
		}
		for _, root := range private {
			if target, e := filepath.EvalSymlinks(root); e == nil {
				root = target
			}
			if root != "" && (gitsource.Beneath(chosen, root) || gitsource.Beneath(root, chosen)) {
				return nil, nil, UserError("wall_files", "the program folder overlaps private state or login files", "Install the program outside private state and login folders.")
			}
		}
		grant, err := files.ResolveGrant(wall.Mount{Host: chosen, Guest: chosen})
		if err != nil {
			return nil, nil, UserError("wall_files", "the program folder cannot be pinned read-only", "Use a stable ordinary installation folder and start again.")
		}
		covered := false
		for _, old := range mounts {
			covered = covered || gitsource.Beneath(grant.Host, old.Host)
		}
		if covered {
			continue
		}
		mounts = slices.DeleteFunc(mounts, func(old wall.Mount) bool { return gitsource.Beneath(old.Host, grant.Host) })
		mounts = append(mounts, grant)
	}
	return mounts, directories, nil
}

func systemProgramDirectory(directory string) bool {
	for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if gitsource.Beneath(directory, root) {
			return true
		}
	}
	return false
}
