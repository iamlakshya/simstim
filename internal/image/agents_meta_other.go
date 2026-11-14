//go:build !linux && !darwin

package image

import "io/fs"

func agentsFileIdentity(fs.FileInfo) (agentsIdentity, error) {
	return agentsIdentity{}, agentsDiskError("this host cannot provide the required inode/change-time source identity")
}
