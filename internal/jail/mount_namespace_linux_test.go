package jail

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNamespaceBindMountFlagsComeFromTheMountedObject(t *testing.T) {
	inMode(t, testMode{"namespaces", true})
	root := hiddenPath(t.TempDir())
	ro, rw := root+"/ro", root+"/rw"
	for _, path := range []string{ro, rw} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	out := jailed(t, Spec{Seccomp: ProfileVMM, Binds: []Bind{{Path: ro}, {Path: rw, Write: true}}}, probeIn{Mounts: []string{ro, rw}})
	if flags, ok := out.Mounts[ro]; !ok || flags&unix.ST_RDONLY == 0 {
		t.Errorf("read-only bind's actual flags = %#x, present=%v", flags, ok)
	}
	if flags, ok := out.Mounts[rw]; !ok || flags&unix.ST_NOEXEC == 0 || flags&unix.ST_RDONLY != 0 {
		t.Errorf("writable bind's actual flags = %#x, present=%v", flags, ok)
	}
}

func TestNamespaceNestedBindLinksStayInsideTheNewRoot(t *testing.T) {
	inMode(t, testMode{"namespaces", true})
	root := hiddenPath(t.TempDir())
	for _, path := range []string{"var", "etc", "run/lock/sub", "run/systemd/resolve", "opt/real"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, link := range map[string]string{"var/lock": "../run/lock", "var/abs": root + "/opt/real",
		"etc/resolv.conf": "../run/systemd/resolve/stub-resolv.conf"} {
		if err := os.Symlink(link, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(root+"/run/systemd/resolve/stub-resolv.conf", []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readPath := root + "/etc/resolv.conf"
	writes := []string{root + "/var/lock/sub/made", root + "/var/abs/made"}
	out := jailed(t, Spec{Seccomp: ProfileVMM, Binds: []Bind{
		{Path: root + "/var", Write: true}, {Path: root + "/var/lock/sub", Write: true}, {Path: root + "/var/abs", Write: true},
		{Path: root + "/etc"}, {Path: readPath},
	}}, probeIn{Read: []string{readPath}, Write: writes})
	if out.Read[readPath] != "ok" {
		t.Errorf("ordinary trailing symlink bind cannot be read: %v", out.Read)
	}
	for _, path := range writes {
		if out.Write[path] != "ok" {
			t.Errorf("ordinary ancestor symlink bind cannot be written: %s: %v", path, out.Write)
		}
	}
	for _, path := range []string{root + "/var", root + "/etc"} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("target creation changed a host ancestor's mode: %s: %v, %v", path, info, err)
		}
	}
}
