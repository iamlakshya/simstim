//go:build linux || darwin

package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMountParserDefaultsAndExplicitAbsoluteTargets(t *testing.T) {
	for _, test := range []struct {
		in, guest string
		write     bool
	}{{"/data", "/mnt/data", false}, {"/data:rw", "/mnt/data", true}, {"/data:/opt/reference:ro", "/opt/reference", false}, {"~/data:~/data:rw", "/home/agent/data", true}} {
		got, err := ParseMount(test.in, "/host/home")
		if err != nil || got.Guest != test.guest || got.Write != test.write {
			t.Fatalf("%s: %+v %v", test.in, got, err)
		}
	}
	for _, value := range []string{"data", "/data:/proc/x", "/data:/run/x:rw", "/data:/mnt/.agent-state-capture", "/data:/../escape", "/data:wrong:mode", "/data::rw", "/data:/mnt/x:rw:extra"} {
		if _, err := ParseMount(value, "/host/home"); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestWritableMountRefusesAncestorAndNestedGitWithoutRunningGit(t *testing.T) {
	base := mountTestDir(t)
	repo := filepath.Join(base, "repo")
	child := filepath.Join(repo, "nested")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{base, repo, child} {
		if f, err := PinMount(name, true); err == nil {
			f.Close()
			t.Fatal("writable repository grant accepted", name)
		}
		if f, err := PinMount(name, false); err != nil {
			t.Fatal(err)
		} else {
			f.Close()
		}
	}
}

func TestMountSourceCannotFollowASymlink(t *testing.T) {
	base := mountTestDir(t)
	outside := mountTestDir(t)
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	for _, write := range []bool{false, true} {
		if f, err := PinMount(alias, write); err == nil {
			f.Close()
			t.Fatal("symlink grant accepted")
		}
	}
}

func TestGrantAllowsContainedAliasAndPinsItsIdentity(t *testing.T) {
	base := mountTestDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink("target", alias); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMount(alias, "/unused")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := ResolveGrant(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Host != target || grant.Identity == "" {
		t.Fatalf("unresolved grant: %+v", grant)
	}
	f, err := PinGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(target, filepath.Join(base, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if f, err := PinGrant(grant); err == nil {
		f.Close()
		t.Fatal("replaced mount identity accepted")
	}
	escape := filepath.Join(base, "escape")
	if err := os.Symlink(mountTestDir(t), escape); err != nil {
		t.Fatal(err)
	}
	parsed, err = ParseMount(escape, "/unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveGrant(parsed); err == nil {
		t.Fatal("grant escaped its named containing root")
	}
}

func mountTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWritableMountRefusesBareGitMetadata(t *testing.T) {
	base := mountTestDir(t)
	bare := filepath.Join(base, "bare")
	for _, name := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(bare, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{bare, base} {
		if f, err := PinMount(name, true); err == nil {
			f.Close()
			t.Fatal("bare Git metadata was granted writable")
		}
		if f, err := PinMount(name, false); err != nil {
			t.Fatal(err)
		} else {
			f.Close()
		}
	}
}
