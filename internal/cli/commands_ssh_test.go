package cli

import (
	"os"
	"os/exec"
	"strings"
)

// The options of decision 0034, as the fake ssh logs them.
const (
	noForwarding = "-o ForwardAgent=no -o ForwardX11=no -o PermitLocalCommand=no -o GSSAPIDelegateCredentials=no"
	noPorts      = "-o ClearAllForwardings=yes"
)

// runGit runs git in dir as a test user.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
