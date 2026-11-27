package client

import (
	"net"
	"os"
	"sync"
)

type agentCollectorSocket struct {
	listener            net.Listener
	path                string
	root, parent        *os.Root
	name                string
	dirInfo, socketInfo os.FileInfo
	retained, borrowed  bool
	once                sync.Once
}

func (s *agentCollectorSocket) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.root != nil && s.socketInfo != nil {
			witness, werr := s.root.Lstat(".owned")
			if current, err := s.root.Lstat("b.sock"); err == nil && agentCollectorSocketOwned(current) && os.SameFile(current, s.socketInfo) && (werr == nil && os.SameFile(current, witness) || !s.retained && s.listener != nil) {
				_ = s.root.Remove("b.sock")
			}
			if werr == nil && agentCollectorSocketOwned(witness) && os.SameFile(witness, s.socketInfo) {
				_ = s.root.Remove(".owned")
			}
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.root != nil && !s.borrowed {
			_ = s.root.Close()
		}
		if s.parent != nil && !s.borrowed {
			if current, err := s.parent.Lstat(s.name); err == nil && agentCollectorDirectoryOwned(current) && s.dirInfo != nil && os.SameFile(current, s.dirInfo) {
				_ = s.parent.Remove(s.name)
			}
			_ = s.parent.Close()
		}
	})
}
