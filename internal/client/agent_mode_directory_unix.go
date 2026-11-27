//go:build darwin || linux

package client

import (
	"os"
	"path/filepath"
)

func newAgentModeDirectory() (_ *agentModeDirectory, err error) {
	d := &agentModeDirectory{}
	keep := false
	defer func() {
		if !keep {
			_ = d.close()
		}
	}()
	dir, err := os.MkdirTemp("", "agent-native-")
	if err != nil {
		return nil, agentNativeLogError("a private folder could not be made")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err == nil && len(real) <= 64 {
		dir = real
	} else {
		_ = os.Remove(dir)
		dir, err = os.MkdirTemp("/tmp", "an-")
		if err != nil {
			return nil, agentNativeLogError("a short private folder could not be made")
		}
		real, err = filepath.EvalSymlinks(dir)
		if err != nil {
			_ = os.Remove(dir)
			return nil, agentNativeLogError("the private folder could not be resolved")
		}
		dir = real
	}
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		_ = os.Remove(dir)
		return nil, agentNativeLogError("the private folder could not be pinned")
	}
	d.parent, d.name = parent, filepath.Base(dir)
	d.info, err = parent.Lstat(d.name)
	if err != nil || !agentNativeOwnedInfo(d.info, os.ModeDir) || d.info.Mode().Perm() != 0o700 {
		return nil, agentNativeLogError("the private folder is unsafe")
	}
	d.root, err = parent.OpenRoot(d.name)
	if err != nil {
		return nil, agentNativeLogError("the private folder could not be opened")
	}
	opened, err := d.root.Stat(".")
	if err != nil || !os.SameFile(opened, d.info) {
		return nil, agentNativeLogError("the private folder changed while opening")
	}
	d.path = dir
	keep = true
	return d, nil
}
