//go:build !windows

package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlIndexRetiresWithoutPinOrUserConfig(t *testing.T) {
	fakeSSH(t)
	cfg := filepath.Join(t.TempDir(), "ssh.config")
	if err := os.WriteFile(cfg, []byte("Host *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "alpha", SSH: "alpha.example", SSHConfig: cfg}
	other := Host{Name: "beta", SSH: "beta.example"}
	if err := SaveHosts(context.Background(), []Host{h, other}); err != nil {
		t.Fatal(err)
	}
	key := newKey(t, "ssh-ed25519")
	for _, name := range []string{h.Name, other.Name} {
		if err := writeTestPin(name, []hostKey{key}); err != nil {
			t.Fatal(err)
		}
	}
	cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	ours := expandedTestSocket(t, cmd.Args)
	cmd, err = (&SSH{Host: other}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	theirs := expandedTestSocket(t, cmd.Args)
	for _, path := range []string{ours, ours + ".0123456789abcdef", theirs} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(pinPath(h.Name), []byte(pinHeader(h.Name)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ours, ours + ".0123456789abcdef"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired socket remains: %s, %v", path, err)
		}
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatalf("another host's socket changed: %v", err)
	}
}

func TestControlRootsDoNotShareOrRetireEachOther(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "scope", SSH: "scope.example"}
	key := newKey(t, "ssh-ed25519")
	makeCommand := func() string {
		t.Helper()
		if err := SaveHosts(context.Background(), []Host{h}); err != nil {
			t.Fatal(err)
		}
		if err := writeTestPin(h.Name, []hostKey{key}); err != nil {
			t.Fatal(err)
		}
		cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
		if err != nil {
			t.Fatal(err)
		}
		return expandedTestSocket(t, cmd.Args)
	}
	ours := makeCommand()
	if err := os.WriteFile(ours, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	theirs := makeCommand()
	if ours == theirs {
		t.Error("independent client roots shared one socket")
	}
	if err := os.WriteFile(theirs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ours); err != nil {
		t.Fatalf("other client root's socket changed: %v", err)
	}
}

func TestControlSharingRequiresItsWriteAheadMarker(t *testing.T) {
	fakeSSH(t)
	pinFor(t, "box")
	dir := sharedDir()
	if err := os.WriteFile(filepath.Join(dir, "h"), []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	if p := sharedPath(cmd.Args); p != "none" {
		t.Fatalf("sharing without marker: %q", cmd.Args)
	}
	if !hasRun(cmd.Args, []string{"-o", "ControlMaster=no"}) {
		t.Fatalf("unsafe fallback: %q", cmd.Args)
	}
}

func TestControlBoundIncludesOpenSSHTemporarySuffix(t *testing.T) {
	for _, uid := range []uint32{0, 501, 1000, ^uint32(0)} {
		dir := controlFallback(uid)
		if !controlFits(dir) {
			t.Fatalf("UID %d fallback does not fit: %s", uid, dir)
		}
		final := len(dir) + 1 + controlKeyLen + 1 + len(controlPrefix) + controlRouteLen
		if final+controlBindExtra > maxSocketPath {
			t.Fatalf("temporary bind path too long: %d", final+controlBindExtra)
		}
	}
	if controlFits(strings.Repeat("x", 16)) {
		t.Fatal("accepted a directory that exceeds the mux temporary bind budget")
	}
}

func TestControlMarkerRefusesForeignShapes(t *testing.T) {
	sshDirs(t)
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := controlDir()
	name := "box"
	key := controlKey(name, nil)
	if markControl(dir, name, key) == "" {
		t.Fatal("could not write marker")
	}
	path := filepath.Join(dir, "h", controlHostKey(name), key)
	if err := os.WriteFile(path, []byte("not empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if markControl(dir, name, key) != "" {
		t.Fatal("accepted nonempty marker")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "foreign"), path); err != nil {
		t.Fatal(err)
	}
	if markControl(dir, name, key) != "" {
		t.Fatal("accepted a linked marker")
	}
}

func TestControlEpochCannotBeReusedAfterForget(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "epoch", SSH: "epoch.example"}
	key := newKey(t, "ssh-ed25519")
	prepare := func() string {
		t.Helper()
		if err := SaveHosts(context.Background(), []Host{h}); err != nil {
			t.Fatal(err)
		}
		if err := writeTestPin(h.Name, []hostKey{key}); err != nil {
			t.Fatal(err)
		}
		cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
		if err != nil {
			t.Fatal(err)
		}
		return expandedTestSocket(t, cmd.Args)
	}
	before := prepare()
	oldDir := filepath.Dir(before)
	if err := ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("retired epoch remains: %v", err)
	}
	if err := os.WriteFile(before, nil, 0o600); !os.IsNotExist(err) {
		t.Fatalf("late master could bind prepared path: %v", err)
	}
	after := prepare()
	if filepath.Dir(after) == oldDir {
		t.Fatal("re-add reused a retired epoch")
	}
	if !privateDir(filepath.Dir(oldDir)) {
		t.Fatal("retirement removed the retained private base")
	}
}

func TestForgetDoesNotRetireAnUnindexedOtherRootsLegacySocket(t *testing.T) {
	fakeSSH(t)
	h := Host{Name: "legacy", SSH: "legacy.example"}
	key := newKey(t, "ssh-ed25519")
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin(h.Name, []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	ours := expandedTestSocket(t, cmd.Args)
	base := filepath.Dir(filepath.Dir(ours))
	foreign := filepath.Join(base, controlPrefix+legacyControlKey(h.Name, []hostKey{key})+"-"+strings.Repeat("2", 40))
	if err := os.WriteFile(foreign, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("unindexed legacy socket from another root was retired: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(ours)); !os.IsNotExist(err) {
		t.Fatalf("own epoch remains: %v", err)
	}
}
