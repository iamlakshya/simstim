package host

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// The copy of .worktreeinclude into a new worktree (spec 19.3). A session
// can write the repository, so the copy treats it as hostile: it never
// follows a symlink out of the repository or the worktree, never blocks on
// a special file, and reads a bounded list.

// The limits of .worktreeinclude, project choices. Over either, the copy
// skips the whole file.
const (
	includeMaxBytes    = 64 << 10
	includeMaxPatterns = 1000
)

// includeFile is the list of files to copy, at the repository's top level.
const includeFile = ".worktreeinclude"

// skippedCopy is an entry of .worktreeinclude that was not copied, and why.
type skippedCopy struct{ Path, Why string }

// afterLstat runs between the Lstat of a file to read and its open. Tests
// swap the file there, as a session could.
var afterLstat = func(string) {}

// copyWorktreeInclude copies the files that .worktreeinclude lists, such as
// .env, which git ignores, from the repository at root into the new worktree
// wt. It reads and writes through os.Root, copies only regular files, and
// never replaces a file that the worktree has. It returns each entry it did
// not copy, and why.
func copyWorktreeInclude(root, wt string) []skippedCopy {
	src, err := os.OpenRoot(root)
	if err != nil {
		return nil
	}
	defer src.Close()
	patterns, skip := includePatterns(src)
	if skip != nil {
		return []skippedCopy{*skip}
	}
	if len(patterns) == 0 {
		return nil
	}
	dst, err := os.OpenRoot(wt)
	if err != nil {
		return []skippedCopy{{includeFile, "the worktree cannot be opened: " + err.Error()}}
	}
	defer dst.Close()
	var skipped []skippedCopy
	done := map[string]bool{}
	for _, line := range patterns {
		names, skips := includeMatches(src, line)
		skipped = append(skipped, skips...)
		for _, name := range names {
			if done[name] {
				continue
			}
			done[name] = true
			if why := copyInto(src, dst, name); why != "" {
				skipped = append(skipped, skippedCopy{name, why})
			}
		}
	}
	return skipped
}

// includePatterns reads the patterns of .worktreeinclude, one a line,
// without empty lines and comments. It reads the file only when it is a
// regular file, and skips all of it when it is over a limit.
func includePatterns(src *os.Root) ([]string, *skippedCopy) {
	st, err := src.Lstat(includeFile)
	if err != nil || !st.Mode().IsRegular() {
		return nil, nil
	}
	tooBig := &skippedCopy{includeFile, fmt.Sprintf("it is larger than %d KiB, so none of it is read", includeMaxBytes>>10)}
	if st.Size() > includeMaxBytes {
		return nil, tooBig
	}
	afterLstat(includeFile)
	f, why := openRegular(src, includeFile, st)
	if f == nil {
		return nil, &skippedCopy{includeFile, why}
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, includeMaxBytes+1))
	switch {
	case err != nil:
		return nil, &skippedCopy{includeFile, "it cannot be read: " + err.Error()}
	case len(b) > includeMaxBytes:
		return nil, tooBig
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(out) == includeMaxPatterns {
			return nil, &skippedCopy{includeFile, fmt.Sprintf("it lists more than %d patterns, so none is copied", includeMaxPatterns)}
		}
		out = append(out, line)
	}
	return out, nil
}

// includeMatches lists the paths in the repository that a line of
// .worktreeinclude names. One leading "/" anchors the pattern to the top
// level, as in .gitignore. A pattern that is not a path inside the
// repository, such as ../x or a/../x, names none, and says why.
func includeMatches(src *os.Root, line string) ([]string, []skippedCopy) {
	p := strings.TrimPrefix(line, "/")
	if !fs.ValidPath(p) || p == "." {
		return nil, []skippedCopy{{line, "the pattern is not a path inside the repository"}}
	}
	return globRoot(src, line, p)
}

// globRoot lists the paths in src that the pattern p matches, as fs.Glob
// does, but one folder at a time. A folder that the walk cannot enter, such
// as a symlink that leaves the repository, is a skipped entry with its
// reason, where fs.Glob would leave it out without a word.
func globRoot(src *os.Root, line, p string) (names []string, skipped []skippedCopy) {
	elems := strings.Split(p, "/")
	dirs := []string{"."}
	for i, elem := range elems {
		var next []string
		for _, dir := range dirs {
			found, why := globElem(src, dir, elem)
			if why != "" {
				skipped = append(skipped, skippedCopy{line, why})
				continue
			}
			if i == len(elems)-1 {
				next = append(next, found...)
				continue
			}
			for _, name := range found {
				st, err := src.Stat(name) // follows a symlink that stays inside
				switch {
				case errors.Is(err, fs.ErrNotExist): // a dangling symlink
				case err != nil:
					skipped = append(skipped, skippedCopy{line, "its folder " + name + " cannot be read inside the repository: " + err.Error()})
				case st.IsDir():
					next = append(next, name)
				}
			}
		}
		dirs = next
	}
	return dirs, skipped
}

// globElem lists the paths in the folder dir whose names elem, one element
// of a pattern, matches. It returns why it cannot, or "".
func globElem(src *os.Root, dir, elem string) ([]string, string) {
	join := func(name string) string {
		if dir == "." {
			return name
		}
		return dir + "/" + name
	}
	if !strings.ContainsAny(elem, `*?[\`) {
		// Lstat: a symlink at the end is a match, and the copy says why it
		// skips it.
		if _, err := src.Lstat(join(elem)); errors.Is(err, fs.ErrNotExist) {
			return nil, ""
		}
		return []string{join(elem)}, ""
	}
	if _, err := path.Match(elem, ""); err != nil {
		return nil, "the pattern is not valid: " + err.Error()
	}
	entries, err := fs.ReadDir(src.FS(), dir)
	if err != nil {
		return nil, "its folder " + dir + " cannot be read inside the repository: " + err.Error()
	}
	var out []string
	for _, e := range entries {
		if ok, _ := path.Match(elem, e.Name()); ok {
			out = append(out, join(e.Name()))
		}
	}
	return out, ""
}

// openRegular opens name in r for reading, where st is its Lstat. It opens
// without blocking, so that a FIFO swapped in after the Lstat cannot hang
// it, and then checks that the open file is the regular file that st
// describes. It returns the file, or why it did not open it.
func openRegular(r *os.Root, name string, st fs.FileInfo) (*os.File, string) {
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "it cannot be read inside the repository: " + err.Error()
	}
	if now, err := f.Stat(); err != nil || !now.Mode().IsRegular() || !os.SameFile(st, now) {
		f.Close()
		return nil, "it changed while it was copied"
	}
	return f, ""
}

// copyInto copies the regular file name from the root src to the same name
// in the root dst, with its permission bits. It returns why it did not, or "".
func copyInto(src, dst *os.Root, name string) string {
	st, err := src.Lstat(name)
	switch {
	case err != nil:
		return "it cannot be read inside the repository: " + err.Error()
	case st.Mode()&fs.ModeSymlink != 0:
		return "it is a symbolic link, and only regular files are copied"
	case !st.Mode().IsRegular():
		return "it is not a regular file"
	}
	afterLstat(name)
	in, why := openRegular(src, name, st)
	if in == nil {
		return why
	}
	defer in.Close()
	if dir := path.Dir(name); dir != "." {
		if err := dst.MkdirAll(dir, 0o700); err != nil {
			return "its folder cannot be made in the worktree: " + err.Error()
		}
	}
	out, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "the worktree already has it"
	}
	if err != nil {
		return "it cannot be written in the worktree: " + err.Error()
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(st.Mode().Perm())
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = dst.Remove(name)
		return "it could not be copied: " + err.Error()
	}
	return ""
}

// logSkippedCopies writes a line to the session's log for each entry of
// .worktreeinclude that its new worktree did not get. The start calls it
// before the holder opens the log.
func logSkippedCopies(id string, g *GitInfo) {
	if g == nil {
		return
	}
	for _, s := range g.skipped {
		warnSession(id, "did not copy an entry of .worktreeinclude into the worktree", "file", s.Path, "why", s.Why)
	}
}
