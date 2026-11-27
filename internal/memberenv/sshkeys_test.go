package memberenv

import (
	"slices"
	"strings"
	"testing"
)

func TestNamedSSHEnvironmentIsClosedAndKeepsExplicitOverrides(t *testing.T) {
	original := []string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}
	closed := NamedSSHEnvironment(original, false)
	if !slices.Equal(closed, original) {
		t.Fatal("closed keys changed the environment")
	}
	open := NamedSSHEnvironment(original, true)
	if len(open) != len(original)+2 || !strings.HasPrefix(open[len(open)-1], "GIT_SSH_COMMAND=ssh -o ") || !strings.Contains(open[len(open)-1], SSHProxyHelperMode) {
		t.Fatal("named SSH transport defaults missing")
	}
	open[0] = "changed"
	if original[0] != "HOME=/home/agent" {
		t.Fatal("projection changed its source")
	}
	for _, override := range []string{"GIT_SSH_COMMAND=custom-command", "GIT_SSH=/custom/ssh"} {
		env := []string{"SSH_AUTH_SOCK=/tmp/custom-agent", override}
		if got := NamedSSHEnvironment(env, true); !slices.Equal(got, env) {
			t.Fatal("explicit sandbox environment did not win")
		}
	}
}

func TestNamedSSHDefaultsRemainPartOfTheActualStoreContext(t *testing.T) {
	base := NamedSSHEnvironment([]string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}, true)
	stored, err := StoreEnvironment(base, nil, nil, "")
	if err != nil || !slices.Equal(stored, base) {
		t.Fatal("named SSH defaults were discarded from the complete Store context", err)
	}
	changed := slices.Clone(base)
	changed[len(changed)-1] = "GIT_SSH_COMMAND=different-command"
	other, err := StoreEnvironment(changed, nil, nil, "")
	if err != nil || slices.Equal(stored, other) {
		t.Fatal("different SSH command collapsed to the same Store context", err)
	}
}
