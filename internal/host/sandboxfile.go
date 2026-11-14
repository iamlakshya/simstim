package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/repopolicy"
)

// ReadSandboxFile reads only the original trusted source tree, never a guest
// clone. The shared reader pins a bounded regular no-follow descriptor.
func ReadSandboxFile(rootPath, repo string) (*api.RepositoryPolicy, error) {
	return repopolicy.Read(rootPath, repo)
}
