//go:build !windows

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func agentBridgeEnv(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(os.TempDir(), "hab-")
	if err != nil {
		t.Fatal(err)
	}
	created := root
	t.Cleanup(func() { _ = os.RemoveAll(created) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, leaf := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
		"XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run",
		"XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs", "GNUPGHOME": "gnupg",
	} {
		dir := filepath.Join(root, leaf)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_AGENT_PID", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(root, "gitconfig-system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GPG_AGENT_INFO", "")
	t.Setenv("GPG_TTY", "")
	t.Setenv("SHELL", "/bin/sh")
	oldSelf, oldNamespaces, oldRequired := SelfCgroup, CgroupNamespaces, selfCgroupRequired
	SelfCgroup = func() ([]byte, error) { return []byte("0::/user.slice/user-1000.slice/session-1.scope\n"), nil }
	CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
	selfCgroupRequired = true
	t.Cleanup(func() { SelfCgroup, CgroupNamespaces, selfCgroupRequired = oldSelf, oldNamespaces, oldRequired })
	return root
}
