package memberenv

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// SSHProxyHelperMode is the fixed guest stdio CONNECT transport.
const SSHProxyHelperMode = "wall-connect"

// NamedSSHHelperPath is the virtual path installed by the existing process,
// gVisor and microvm helper mounts. The real guest checks its actual selected
// executable against this path before applying the environment projection.
func NamedSSHHelperPath() string { return filepath.Join("/opt", brand.Name, "bin", brand.HostBinary) }

// NamedSSHEnvironment projects the same complete environment for trusted
// Store planning and guest execution. It performs no I/O and grants no keys.
// Explicit sandbox.env values retain their established precedence.
func NamedSSHEnvironment(input []string, enabled bool) []string {
	env := slices.Clone(input)
	if !enabled {
		return env
	}
	if !sshEnvPresent(env, "SSH_AUTH_SOCK") {
		env = append(env, "SSH_AUTH_SOCK="+filepath.Join("/run", brand.Name, "ssh-agent.sock"))
	}
	if !sshEnvPresent(env, "GIT_SSH_COMMAND") && !sshEnvPresent(env, "GIT_SSH") {
		proxy := sshShellQuote(NamedSSHHelperPath()) + " " + SSHProxyHelperMode + " %h %p"
		env = append(env, "GIT_SSH_COMMAND=ssh -o "+sshShellQuote("ProxyCommand="+proxy))
	}
	return env
}

func sshShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func sshEnvPresent(env []string, name string) bool {
	for _, value := range env {
		if strings.HasPrefix(value, name+"=") {
			return true
		}
	}
	return false
}
