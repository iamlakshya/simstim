package host

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/image"
)

type installedAgentsCapture struct {
	inputs   []image.AgentsInputV1
	binaries []image.AgentBinaryV1
	kinds    []string
	close    []func() error
}

func (c *installedAgentsCapture) Close() {
	for i := len(c.close) - 1; i >= 0; i-- {
		_ = c.close[i]()
	}
}

// captureInstalledAgents reads only the canonical table's declared install
// roots and one explicitly resolved binary. It never executes as validation,
// walks credential folders, requires UID0, or upgrades these bytes into G11.
func captureInstalledAgents(ctx context.Context, home, search string) (capture *installedAgentsCapture, returned error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		return nil, agentsBuildFailure("the canonical agent home is invalid")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	capture = &installedAgentsCapture{}
	ownedCapture := capture
	defer func() {
		if returned != nil {
			ownedCapture.Close()
		}
	}()
	paths := map[string]bool{}
	links := map[string]string{}
	for _, agent := range agents.Table {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		selected, name, installed := installedAgentLocation(agent, search, home)
		if !installed {
			continue
		}
		resolved, err := filepath.EvalSymlinks(selected)
		if err != nil {
			return nil, agentsBuildFailure("an installed binary cannot be resolved")
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		file, err := captureAgentExecutable(resolved)
		if err != nil {
			return nil, err
		}
		if paths[resolved] {
			file.Close()
		} else {
			capture.inputs = append(capture.inputs, image.AgentsInputV1{HostPath: resolved, File: file})
			capture.close = append(capture.close, file.Close)
			paths[resolved] = true
		}
		if previous, exists := links[name]; exists && previous != resolved {
			return nil, agentsBuildFailure("installed agents select conflicting bin names")
		}
		if _, exists := links[name]; !exists {
			capture.binaries = append(capture.binaries, image.AgentBinaryV1{Name: name, ResolvedPath: resolved})
			links[name] = resolved
		}
		capture.kinds = append(capture.kinds, agent.Kind)
		for _, relative := range agent.InstallDirs {
			if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\\x00\r\n") {
				return nil, agentsBuildFailure("the canonical install folder is not home-relative")
			}
			absolute := filepath.Join(home, filepath.FromSlash(relative))
			if paths[absolute] {
				continue
			}
			before, err := root.Lstat(relative)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if before.Mode()&os.ModeSymlink != 0 {
				target, err := root.Readlink(relative)
				if err != nil {
					return nil, err
				}
				after, err := root.Lstat(relative)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
					return nil, agentsBuildFailure("an installed folder link changed")
				}
				capture.inputs = append(capture.inputs, image.AgentsInputV1{HostPath: absolute, LinkInfo: before, LinkTarget: target})
				paths[absolute] = true
				continue
			}
			if !before.IsDir() {
				return nil, agentsBuildFailure("a declared install folder is not a directory")
			}
			folder, err := root.OpenRoot(relative)
			if err != nil {
				return nil, err
			}
			after, err := folder.Stat(".")
			if err != nil || !os.SameFile(before, after) {
				folder.Close()
				return nil, agentsBuildFailure("an installed folder changed identity")
			}
			capture.inputs = append(capture.inputs, image.AgentsInputV1{HostPath: absolute, Folder: folder})
			capture.close = append(capture.close, folder.Close)
			paths[absolute] = true
		}
	}
	return capture, nil
}

// captureAgentExecutable has the role-specific OUTSIDE-G11 policy: an actual
// rooted, regular readable executable, without special-file blocking or a
// final symlink race. Canonical files.OpenRegular supplies the common native
// no-follow/descriptor primitive. Owner may be the user or an OS installation.
func captureAgentExecutable(resolved string) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	leaf := filepath.Base(resolved)
	before, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	file, err := files.OpenRegular(root, leaf)
	if err != nil {
		return nil, agentsBuildFailure("the resolved installed binary is not a safe regular file")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm()&0o111 == 0 {
		file.Close()
		return nil, agentsBuildFailure("the installed executable changed identity or has no execute permission")
	}
	return file, nil
}

func installedAgentLocation(agent agents.Agent, search, home string) (string, string, bool) {
	for _, name := range agent.Binaries {
		if found, err := lookPath(name, search); err == nil {
			return found, filepath.Base(name), true
		}
	}
	if agent.Setup != nil {
		for _, name := range agent.Setup.Paths {
			candidate := agents.HomePath(name, home)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				return candidate, filepath.Base(candidate), true
			}
		}
	}
	return "", "", false
}
func agentsBuildFailure(detail string) error {
	return UserError("agents_disk", "could not build the agents disk: "+detail, "Check the installed agents with host doctor, then try again.")
}
