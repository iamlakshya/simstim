package image

import (
	"io/fs"
	"syscall"
)

func agentsFileIdentity(info fs.FileInfo) (agentsIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return agentsIdentity{}, agentsDiskError("the installed file has no Linux inode/change-time identity")
	}
	return agentsIdentity{device: uint64(stat.Dev), inode: stat.Ino, seconds: stat.Ctim.Sec, nanoseconds: stat.Ctim.Nsec}, nil
}
