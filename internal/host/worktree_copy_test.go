package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// put writes a file with its mode, making its folder.
func put(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // past the umask
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func missing(p string) bool {
	_, err := os.Lstat(p)
	return errors.Is(err, fs.ErrNotExist)
}

// skipReasons maps each skipped entry to its reasons.
func skipReasons(skipped []skippedCopy) map[string]string {
	why := map[string]string{}
	for _, s := range skipped {
		why[s.Path] += s.Why + "\n"
	}
	return why
}

// Spec 19.3, test 1: the copy of .worktreeinclude never follows a symlink
// out of the repository or the worktree, and never replaces a file there.
// Each entry that it skips comes back with its reason.
func TestCopyWorktreeInclude(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root, wt, out := filepath.Join(base, "repo"), filepath.Join(base, "wt"), filepath.Join(base, "outside")
	put(t, filepath.Join(out, ".ssh", "id_ed25519"), "PRIVATE KEY", 0o600)
	put(t, filepath.Join(out, "dir", "x.env"), "outside", 0o644)
	put(t, filepath.Join(root, ".env"), "A=1\n", 0o640)
	put(t, filepath.Join(root, "config", "local.env"), "B=2\n", 0o600)
	put(t, filepath.Join(root, "anchored.env"), "C=3\n", 0o644)
	put(t, filepath.Join(root, "taken.env"), "mine\n", 0o644)
	put(t, filepath.Join(root, "through.env"), "through\n", 0o644)
	put(t, filepath.Join(root, "out", "w.env"), "into a folder out\n", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(out, ".ssh", "id_ed25519"), filepath.Join(root, "key"))
	symlink(t, "../outside/.ssh/id_ed25519", filepath.Join(root, "rel"))
	symlink(t, filepath.Join(out, "dir"), filepath.Join(root, "linked"))
	symlink(t, ".env", filepath.Join(root, "inside.env"))
	put(t, filepath.Join(wt, "taken.env"), "theirs\n", 0o644)
	symlink(t, filepath.Join(out, "target"), filepath.Join(wt, "through.env"))
	if err := os.MkdirAll(filepath.Join(out, "wdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(out, "wdir"), filepath.Join(wt, "out")) // a folder of the worktree that leaves it
	put(t, filepath.Join(root, includeFile), strings.Join([]string{
		"# files git ignores", "", ".env", "config/*.env", "/anchored.env", "/etc/passwd", "key", "rel",
		"linked/x.env", "linked/*.env", "inside.env", "taken.env", "through.env", "out/w.env",
		"../x", "./.env", "config/../.env", "//etc/passwd", "sub", "missing.env",
	}, "\n")+"\n", 0o644)

	skipped := copyWorktreeInclude(root, wt)

	for name, want := range map[string]string{".env": "A=1\n", "config/local.env": "B=2\n", "anchored.env": "C=3\n", "taken.env": "theirs\n"} {
		if b, err := os.ReadFile(filepath.Join(wt, name)); err != nil || string(b) != want {
			t.Errorf("%s in the worktree = %q, %v; want %q", name, b, err, want)
		}
	}
	for name, mode := range map[string]os.FileMode{".env": 0o640, "config/local.env": 0o600} {
		if st, err := os.Stat(filepath.Join(wt, name)); err != nil || st.Mode().Perm() != mode {
			t.Errorf("%s has mode %v, want %v (%v)", name, st.Mode().Perm(), mode, err)
		}
	}
	for _, name := range []string{"key", "rel", "linked", "inside.env", "sub", "missing.env", "etc"} {
		if !missing(filepath.Join(wt, name)) {
			t.Errorf("%s was copied into the worktree", name)
		}
	}
	for _, p := range []string{filepath.Join(out, "target"), filepath.Join(out, "wdir", "w.env")} {
		if !missing(p) {
			t.Errorf("the copy wrote through a symlink in the worktree: %s", p)
		}
	}
	why := skipReasons(skipped)
	for name, want := range map[string]string{
		"key": "symbolic link", "rel": "symbolic link", "inside.env": "symbolic link",
		"linked/x.env": "its folder linked cannot be read inside the repository", "linked/*.env": "its folder linked cannot be read inside the repository",
		"taken.env": "already has it", "through.env": "already has it", "out/w.env": "its folder cannot be made in the worktree",
		"../x": "not a path inside", "./.env": "not a path inside", "config/../.env": "not a path inside", "//etc/passwd": "not a path inside",
		"sub": "not a regular file",
	} {
		if !strings.Contains(why[name], want) {
			t.Errorf("skipped %s: %q, want %q", name, why[name], want)
		}
	}
	for _, name := range []string{".env", "config/local.env", "anchored.env", "/anchored.env", "/etc/passwd", "etc/passwd", "missing.env"} {
		if _, ok := why[name]; ok {
			t.Errorf("%s is listed as skipped: %q", name, why[name])
		}
	}

	// A .worktreeinclude that is a symlink is not read at all.
	root2, wt2 := filepath.Join(base, "repo2"), filepath.Join(base, "wt2")
	put(t, filepath.Join(root2, ".env"), "A=1\n", 0o644)
	put(t, filepath.Join(out, "list"), ".env\n", 0o644)
	symlink(t, filepath.Join(out, "list"), filepath.Join(root2, includeFile))
	if err := os.MkdirAll(wt2, 0o755); err != nil {
		t.Fatal(err)
	}
	if s := copyWorktreeInclude(root2, wt2); s != nil || !missing(filepath.Join(wt2, ".env")) {
		t.Errorf("a symlinked .worktreeinclude was read: %+v", s)
	}
}

// A .worktreeinclude over 64 KiB, even a sparse one of 64 GiB, or with more
// than 1000 patterns, is skipped whole, with one entry that says why.
func TestCopyWorktreeIncludeLimits(t *testing.T) {
	base := t.TempDir()
	root, wt := filepath.Join(base, "repo"), filepath.Join(base, "wt")
	put(t, filepath.Join(root, ".env"), "A=1\n", 0o644)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(root, includeFile)
	put(t, list, ".env\n", 0o644)
	if err := os.Truncate(list, 64<<30); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if s := copyWorktreeInclude(root, wt); len(s) != 1 || s[0].Path != includeFile || !strings.Contains(s[0].Why, "larger than 64 KiB") || time.Since(start) > 5*time.Second {
		t.Errorf("a sparse .worktreeinclude of 64 GiB: %+v after %s", s, time.Since(start))
	}
	lines := []string{".env"}
	for i := len(lines); i < includeMaxPatterns; i++ {
		lines = append(lines, fmt.Sprintf("missing-%d", i))
	}
	put(t, list, strings.Join(lines, "\n")+"\n", 0o644)
	if s := copyWorktreeInclude(root, wt); s != nil || missing(filepath.Join(wt, ".env")) {
		t.Errorf("1000 patterns: %+v", s)
	}
	_ = os.Remove(filepath.Join(wt, ".env"))
	put(t, list, strings.Join(append(lines, "one-more"), "\n")+"\n", 0o644)
	if s := copyWorktreeInclude(root, wt); len(s) != 1 || !strings.Contains(s[0].Why, "more than 1000 patterns") || !missing(filepath.Join(wt, ".env")) {
		t.Errorf("1001 patterns: %+v", s)
	}
	// A file that grows after the Lstat, here to a sparse 256 MiB, is still
	// read to 64 KiB only: the copy does not hold the rest in memory.
	put(t, list, ".env\n", 0o644)
	old := afterLstat
	defer func() { afterLstat = old }()
	afterLstat = func(name string) {
		if name == includeFile {
			if err := os.Truncate(list, 256<<20); err != nil {
				t.Error(err)
			}
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s := copyWorktreeInclude(root, wt)
	runtime.ReadMemStats(&after)
	if len(s) != 1 || !strings.Contains(s[0].Why, "larger than 64 KiB") || !missing(filepath.Join(wt, ".env")) {
		t.Errorf("a .worktreeinclude that grew after the Lstat: %+v", s)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("the copy allocated %d MiB for a .worktreeinclude that grew", n>>20)
	}
}

// warnSession writes through the log's turnover, so a line that would take
// holder.log past its size starts a new one.
func TestWarnSessionTurnsOverTheLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	id := "abcd2345"
	logPath := paths.HolderLog(id)
	put(t, logPath, strings.Repeat("x", holderLogMax-10)+"\n", 0o600)
	logSkippedCopies(id, &GitInfo{skipped: []skippedCopy{{"id", "it is a symbolic link"}}})
	if st, err := os.Stat(logPath + ".1"); err != nil || st.Size() != holderLogMax-9 {
		t.Errorf("holder.log.1: %v, %v", st, err)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), `level=WARN msg="did not copy an entry of .worktreeinclude into the worktree" file=id why="it is a symbolic link"`) || strings.Count(string(b), "\n") != 1 {
		t.Errorf("holder.log = %q", b)
	}
}

// A FIFO that a session swaps in between the Lstat and the open, of
// .worktreeinclude or of a file it lists, cannot hang the copy.
func TestCopyWorktreeIncludeFIFORace(t *testing.T) {
	for _, swapped := range []string{includeFile, ".env"} {
		t.Run(swapped, func(t *testing.T) {
			base := t.TempDir()
			root, wt := filepath.Join(base, "repo"), filepath.Join(base, "wt")
			put(t, filepath.Join(root, ".env"), "A=1\n", 0o644)
			put(t, filepath.Join(root, includeFile), ".env\n", 0o644)
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			fifo := filepath.Join(root, swapped)
			old := afterLstat
			afterLstat = func(name string) {
				if name == swapped {
					_ = os.Remove(fifo)
					if err := syscall.Mkfifo(fifo, 0o644); err != nil {
						t.Error(err)
					}
				}
			}
			defer func() { afterLstat = old }()
			done := make(chan []skippedCopy, 1)
			go func() { done <- copyWorktreeInclude(root, wt) }()
			select {
			case s := <-done:
				if len(s) != 1 || s[0].Path != swapped || s[0].Why != "it changed while it was copied" || !missing(filepath.Join(wt, ".env")) {
					t.Errorf("skipped = %+v", s)
				}
			case <-time.After(10 * time.Second):
				// Let the blocked open go, so the test can end.
				if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					f.Close()
				}
				t.Fatal("the copy blocked on a FIFO")
			}
		})
	}
}
