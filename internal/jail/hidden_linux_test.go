package jail

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestForbiddenDevicesWithoutLoadedVhostNodes(t *testing.T) {
	for _, path := range []string{"/dev", "/dev/vhost-net", "/dev/vhost-vsock", "/dev/vhost-future/child"} {
		if why := forbidden(path, hidden{}); why == "" {
			t.Errorf("forbidden(%q) accepted a device path without any loaded node", path)
		}
	}
	if why := forbidden("/dev/null", hidden{}); why != "" {
		t.Errorf("forbidden(/dev/null) = %q, want an allowed explicit device", why)
	}
}

func TestProtectedPathsKeepMissingHomesAndRuntime(t *testing.T) {
	root := hiddenPath(t.TempDir())
	h := protectedPaths(root+"/env/missing/home", root+"/login/missing/home", root+"/run/missing", nil)
	for _, path := range []string{root + "/env", root + "/login", root + "/run"} {
		if why := forbidden(path, h); why == "" {
			t.Errorf("forbidden(%q) accepted an ancestor of a protected missing path", path)
		}
	}
	if why := forbidden(root+"/project", h); why != "" {
		t.Errorf("an unrelated explicit project bind was refused: %s", why)
	}
}

func TestProtectedPathsResolveExistingAncestorsAndVhostAliases(t *testing.T) {
	root := hiddenPath(t.TempDir())
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	h := protectedPaths(alias+"/missing/home", "", alias+"/missing/run", []string{alias + "/vhost-node"})
	for _, path := range []string{real, real + "/vhost-node"} {
		if why := forbidden(path, h); why == "" {
			t.Errorf("forbidden(%q) lost the resolved protected alias", path)
		}
	}
	// A vhost link can point outside /dev and outside either protected home.
	node := filepath.Join(root, "backing")
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "vhost-link")
	if err := os.Symlink(node, link); err != nil {
		t.Fatal(err)
	}
	h = protectedPaths("", "", "", []string{link})
	for _, path := range []string{node, root} {
		if why := forbidden(path, h); why == "" {
			t.Errorf("forbidden(%q) accepted the target of a vhost alias", path)
		}
	}
}

func TestHiddenPathsProtectsLoginHomeWithoutHOME(t *testing.T) {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || u.HomeDir == "" {
		t.Skipf("no login home is recorded: %v", err)
	}
	t.Setenv("HOME", "")
	h := hiddenPaths()
	want := hiddenPath(u.HomeDir)
	if !slices.Contains(h.homes, want) || forbidden(want, h) == "" {
		t.Fatalf("unset HOME lost the login home: protected=%v, login=%q", h.homes, want)
	}

	other := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("HOME", other)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "run-not-created"))
	h = hiddenPaths()
	if !slices.Contains(h.homes, want) || !slices.Contains(h.homes, hiddenPath(other)) {
		t.Errorf("different environment and login homes were not both protected: %v", h.homes)
	}
	if why := forbidden(filepath.Dir(other), h); !strings.Contains(why, "home") {
		t.Errorf("missing HOME parent refusal = %q", why)
	}
	if why := forbidden(filepath.Dir(hiddenPath(os.Getenv("XDG_RUNTIME_DIR"))), h); !strings.Contains(why, "XDG_RUNTIME_DIR") {
		t.Errorf("missing runtime parent refusal = %q", why)
	}
}

func TestProtectedPathsKeepExistingAndDanglingLinkEntries(t *testing.T) {
	for _, category := range []struct {
		name string
		hide func(string) hidden
	}{
		{"home", func(p string) hidden { return protectedPaths(p, "", "", nil) }},
		{"login", func(p string) hidden { return protectedPaths("", p, "", nil) }},
		{"runtime", func(p string) hidden { return protectedPaths("", "", p, nil) }},
		{"vhost", func(p string) hidden { return protectedPaths("", "", "", []string{p}) }},
	} {
		t.Run(category.name, func(t *testing.T) {
			for _, exists := range []bool{false, true} {
				t.Run("exists="+strconv.FormatBool(exists), func(t *testing.T) {
					root := hiddenPath(t.TempDir())
					entryDir, targetDir := root+"/links", root+"/targets"
					for _, p := range []string{entryDir, targetDir, entryDir + "/unrelated", root + "/project"} {
						if err := os.Mkdir(p, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					entry, target := entryDir+"/configured", targetDir+"/protected"
					if exists {
						if err := os.Mkdir(target, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(target, entry); err != nil {
						t.Fatal(err)
					}
					h := category.hide(entry)
					if h.err != nil {
						t.Fatalf("an ordinary protected link was refused: %v", h.err)
					}
					for _, p := range []string{entryDir, targetDir, target} {
						if why := forbidden(p, h); why == "" {
							t.Errorf("protected link left %s bindable", p)
						}
					}
					for _, p := range []string{entryDir + "/unrelated", root + "/project"} {
						if why := forbidden(p, h); why != "" {
							t.Errorf("ordinary sibling %s was refused: %s", p, why)
						}
					}
				})
			}
		})
	}
}

func TestProtectedPathsKeepEachIntermediateLinkHop(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, dir := range []string{"source", "hop", "real", "final", "project"} {
		if err := os.Mkdir(root+"/"+dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for entry, target := range map[string]string{
		root + "/source/alias": root + "/hop",
		root + "/hop/home":     "../real/second",
		root + "/real/second":  root + "/final/missing/home",
		root + "/ordinary":     root + "/project",
	} {
		if err := os.Symlink(target, entry); err != nil {
			t.Fatal(err)
		}
	}
	h := protectedPaths(root+"/source/alias/home", "", "", nil)
	if h.err != nil {
		t.Fatalf("ordinary multi-hop links were refused: %v", h.err)
	}
	for _, p := range []string{root + "/source", root + "/hop", root + "/real", root + "/final"} {
		if why := forbidden(p, h); why == "" {
			t.Errorf("a protected intermediate link left %s bindable", p)
		}
	}
	if why := forbidden(hiddenPath(root+"/ordinary"), h); why != "" {
		t.Errorf("an ordinary unrelated link was refused: %s", why)
	}
}

func TestProtectedPathsKeepLinkSensitiveParentComponents(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, p := range []string{root + "/input", root + "/real/deep", root + "/ordinary/dir"} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entry := root + "/input/link"
	if err := os.Symlink(root+"/real/deep", entry); err != nil {
		t.Fatal(err)
	}
	// Joining or cleaning this before following link would choose input/home.
	h := protectedPaths(entry+"/../home", "", "", nil)
	if h.err != nil || forbidden(root+"/real", h) == "" {
		t.Fatalf("a parent component lost the link's actual target: %v, %+v", h.err, h.homes)
	}
	if why := forbidden(root+"/input/home", h); why != "" {
		t.Errorf("the untraversed lexical parent target was overprotected: %s", why)
	}
	h = protectedPaths(root+"/ordinary/dir/../home", "", "", nil)
	if h.err != nil {
		t.Fatal(h.err)
	}
	if why := forbidden(root+"/ordinary/dir", h); why != "" {
		t.Errorf("an existing ordinary component before .. was overprotected: %s", why)
	}
	if why := forbidden(root+"/ordinary", h); why == "" {
		t.Error("the actual home parent remains bindable")
	}
}

func TestProtectedPathsResolveRelativeConfiguration(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, p := range []string{root + "/links", root + "/targets"} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../targets/missing", root+"/links/home"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	h := protectedPaths("links/home", "", "", nil)
	if h.err != nil || forbidden(root+"/links", h) == "" || forbidden(root+"/targets", h) == "" {
		t.Fatalf("relative protected paths lost an entry or target: %v, %+v", h.err, h.homes)
	}
}

func TestProtectedPathsKeepMissingEntryParentsBeforeDotDot(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, dir := range []string{"source", "destination", "project"} {
		if err := os.Mkdir(root+"/"+dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The missing entry's existing parent matters even though the remaining
	// components lead to a different final protected destination.
	h := protectedPaths(root+"/source/missing/../../destination/home", "", "", nil)
	if h.err != nil {
		t.Fatal(h.err)
	}
	for _, p := range []string{root + "/source", root + "/destination"} {
		if why := forbidden(p, h); why == "" {
			t.Errorf("missing protected components left %s bindable", p)
		}
	}
	if why := forbidden(root+"/project", h); why != "" {
		t.Errorf("an unrelated existing sibling was refused: %s", why)
	}
}

func TestProtectedPathsRefuseUnverifiablePaths(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Symlink("second", root+"/first"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("first", root+"/second"); err != nil {
		t.Fatal(err)
	}
	file := root + "/file"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, path string
		want       error
	}{
		{"cycle", root + "/first", syscall.ELOOP},
		{"invalid lookup", root + "/bad\x00path", syscall.EINVAL},
		{"not a directory", file + "/child", syscall.ENOTDIR},
		{"file with trailing slash", file + "/", syscall.ENOTDIR},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := protectedPaths(c.path, "", "", nil)
			if !errors.Is(h.err, c.want) {
				t.Fatalf("unverifiable protected path error = %v, want %v", h.err, c.want)
			}
			if why := forbidden(root+"/project", h); why == "" {
				t.Error("a failed protection lookup allowed a bind")
			}
		})
	}
	// No filesystem bind can expose a protected path in a no-bind converter.
	t.Setenv("HOME", root+"/first")
	p, err := pin(checked{}, 0)
	p.close()
	if err != nil {
		t.Fatalf("an unresolved home disabled a no-bind converter: %v", err)
	}
}

func TestProtectedPathsBoundLinksWithoutRejectingOrdinaryChains(t *testing.T) {
	for _, count := range []int{40, 41} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			root := hiddenPath(t.TempDir())
			for i := range count {
				target := "missing"
				if i+1 < count {
					target = "link" + strconv.Itoa(i+1)
				}
				if err := os.Symlink(target, root+"/link"+strconv.Itoa(i)); err != nil {
					t.Fatal(err)
				}
			}
			h := protectedPaths(root+"/link0", "", "", nil)
			if count == 40 {
				if h.err != nil || forbidden(root, h) == "" || forbidden(root+"/project", h) != "" {
					t.Fatalf("an ordinary bounded chain was refused or lost protection: %v, %+v", h.err, h.homes)
				}
			} else if !errors.Is(h.err, syscall.ELOOP) || forbidden(root+"/project", h) == "" {
				t.Fatalf("an overlong chain did not fail closed: %v", h.err)
			}
		})
	}
}

// hiddenPath is only a canonical fixture helper. Security protection uses
// protectedHops and keeps all variants and lookup errors instead.
func hiddenPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	clean := filepath.Clean(abs)
	var tail []string
	for {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return clean
		}
		tail = append(tail, filepath.Base(abs))
		abs = parent
	}
}
