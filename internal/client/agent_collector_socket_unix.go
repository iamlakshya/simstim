//go:build !windows

package client

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func newAgentCollectorSocket() (*agentCollectorSocket, error) {
	dir, err := os.MkdirTemp("/tmp", "acb-")
	if err != nil {
		return nil, agentBindingProofError()
	}
	s := &agentCollectorSocket{name: filepath.Base(dir), path: filepath.Join(dir, "b.sock")}
	keep := false
	defer func() {
		if !keep {
			s.Close()
		}
	}()
	s.parent, err = os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, agentBindingProofError()
	}
	s.dirInfo, err = s.parent.Lstat(s.name)
	if err != nil || !privateDir(dir) {
		return nil, agentBindingProofError()
	}
	s.root, err = s.parent.OpenRoot(s.name)
	if err != nil {
		return nil, agentBindingProofError()
	}
	opened, err := s.root.Stat(".")
	if err != nil || !os.SameFile(opened, s.dirInfo) || len(s.path) > maxSocketPath {
		return nil, agentBindingProofError()
	}
	if err := finishAgentCollectorSocket(s); err != nil {
		return nil, err
	}
	keep = true
	return s, nil
}

func newAgentCollectorSocketAtDirectory(directory *agentModeDirectory) (*agentCollectorSocket, error) {
	if err := directory.check(); err != nil {
		return nil, err
	}
	s := &agentCollectorSocket{root: directory.root, parent: directory.parent,
		name: directory.name, dirInfo: directory.info, path: filepath.Join(directory.path, "b.sock"), borrowed: true}
	if len(s.path) > maxSocketPath {
		return nil, agentBindingProofError()
	}
	if err := finishAgentCollectorSocket(s); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func finishAgentCollectorSocket(s *agentCollectorSocket) error {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.path, Net: "unix"})
	if err != nil {
		return agentBindingProofError()
	}
	listener.SetUnlinkOnClose(false)
	s.listener = listener
	s.socketInfo, err = s.root.Lstat("b.sock")
	if err != nil || !agentCollectorSocketOwned(s.socketInfo) {
		return agentBindingProofError()
	}
	if err := s.root.Link("b.sock", ".owned"); err != nil {
		return agentBindingProofError()
	}
	s.retained = true
	retained, err := s.root.Lstat(".owned")
	if err != nil || !os.SameFile(retained, s.socketInfo) {
		return agentBindingProofError()
	}
	s.socketInfo, err = s.root.Lstat(".owned")
	if err != nil || !agentCollectorSocketOwned(s.socketInfo) {
		return agentBindingProofError()
	}
	current, err := s.root.Lstat("b.sock")
	if err != nil || !os.SameFile(current, s.socketInfo) {
		return agentBindingProofError()
	}
	if err := chmodCollectorSocket(s.root, ".owned", s.socketInfo); err != nil {
		return err
	}
	return nil
}

func agentCollectorSocketOwned(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid())
}

func agentCollectorDirectoryOwned(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid())
}

func agentCollectorOwnUID(uid uint32) bool { return uid == uint32(os.Getuid()) }
