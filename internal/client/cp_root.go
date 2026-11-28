package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
)

// DefaultCPMaxBytes caps one `cp` command (256 MiB, spec 21.1).
const DefaultCPMaxBytes int64 = 256 << 20

// DefaultCPMaxEntries caps the entries copied by one `cp` command (10,000, spec 21.1).
const DefaultCPMaxEntries = 10_000

// validateRelEntry checks a relative slash-separated path from a remote
// session against spec 21.1's name rules: no empty/dot/dotdot parts, no NUL or
// control characters, no `.git` part, and unchanged by api.CleanText.
func validateRelEntry(rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	if strings.ContainsRune(rel, '\\') || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return &UserError{
			Msg:  fmt.Sprintf("refused path %q from the session", CleanLine(rel)),
			Hint: "Copy files with plain relative names.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	if api.CleanText(rel, len(rel)+1) != rel {
		return &UserError{
			Msg:  fmt.Sprintf("refused path %q: it contains control or hidden characters", CleanLine(rel)),
			Hint: "Rename the file in the session before copying it.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsRune(seg, 0) || strings.ContainsFunc(seg, unicode.IsControl) {
			return &UserError{
				Msg:  fmt.Sprintf("refused path %q: %q is not a safe file name", CleanLine(rel), CleanLine(seg)),
				Hint: "Copy files with plain names inside the target folder.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
		if strings.EqualFold(seg, ".git") {
			return &UserError{
				Msg:  CleanLine(rel) + " would land in a .git folder on your laptop",
				Hint: "Copy the files you need, without their .git folder.",
				Code: "cp_git",
				Kind: KindRefused,
			}
		}
	}
	return nil
}

// checkCaseCollisions refuses any two entries whose paths differ only in case,
// because case-insensitive file systems cannot tell them apart (spec 21.1).
func checkCaseCollisions(rels []string) error {
	seen := make(map[string]string, len(rels))
	for _, r := range rels {
		low := strings.ToLower(r)
		if prev, ok := seen[low]; ok && prev != r {
			return &UserError{
				Msg:  fmt.Sprintf("refused %q and %q: names differ only in case", CleanLine(prev), CleanLine(r)),
				Hint: "Rename one of the files in the session before copying.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
		seen[low] = r
	}
	return nil
}

// ensureNoSymlinkAncestors checks every directory prefix of rel inside root
// using root.Lstat so a symlink inside the destination tree (for example one
// pointing to .git) is never followed (spec 21.1).
func ensureNoSymlinkAncestors(root *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	cur := ""
	for i, seg := range parts {
		if i == 0 {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		st, err := root.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return &UserError{
				Msg:  fmt.Sprintf("cannot inspect destination %s: %v", CleanPath(cur), err),
				Hint: "Check permissions on the destination folder.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
		isLast := i == len(parts)-1
		if st.Mode()&fs.ModeSymlink != 0 && !isLast {
			return &UserError{
				Msg:  fmt.Sprintf("refused destination %s: %s is a symbolic link", CleanPath(rel), CleanPath(cur)),
				Hint: "Copy into a plain folder without symbolic links.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
	}
	return nil
}

// mkdirAllNoSymlink creates each directory component of dir inside root,
// refusing any existing symlink along the way. When created is non-nil, each
// newly created directory path is appended in parent-before-child order so a
// failed copy can remove empty directories it created.
func mkdirAllNoSymlink(root *os.Root, dir string, created *[]string) error {
	if dir == "" || dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for i, seg := range parts {
		if i == 0 {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		st, err := root.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := root.Mkdir(cur, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
				return fmt.Errorf("create folder %s: %w", cur, mkErr)
			}
			if created != nil {
				*created = append(*created, cur)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect folder %s: %w", cur, err)
		}
		if st.Mode()&fs.ModeSymlink != 0 || !st.IsDir() {
			return &UserError{
				Msg:  fmt.Sprintf("refused destination folder %s: it is not a plain folder", CleanPath(cur)),
				Hint: "Remove the conflicting file or symbolic link, or copy somewhere else.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
	}
	return nil
}

// isSystemRootSymlink reports whether p is an OS-owned top-level directory
// symlink (such as /tmp -> private/tmp or /var -> private/var on macOS).
// Tests replace it to exercise macOS /tmp semantics on Linux.
var isSystemRootSymlink = func(p string) bool {
	return filepath.Dir(p) == string(filepath.Separator) && p != string(filepath.Separator)
}

func checkNoGitSegment(p, display string) error {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(seg, ".git") {
			return &UserError{
				Msg:  CleanPath(display) + " would land in a .git folder on your laptop",
				Hint: "Copy the files you need, without their .git folder.",
				Code: "cp_git",
				Kind: KindRefused,
			}
		}
	}
	return nil
}

// resolveSystemDir resolves p when it is an OS top-level directory symlink
// (such as /tmp on macOS) and returns the real directory path. For any other
// symlink it returns ok=false so planted symlinks are refused.
func resolveSystemDir(p string) (resolved string, ok bool, err error) {
	if !isSystemRootSymlink(p) {
		return "", false, nil
	}
	st, serr := os.Stat(p)
	if serr == nil && st.IsDir() {
		realPath, rerr := filepath.EvalSymlinks(p)
		if rerr != nil {
			return "", false, fmt.Errorf("resolve destination folder %s: %w", p, rerr)
		}
		return realPath, true, nil
	}
	return "", false, nil
}

// openRootForTarget opens an os.Root on a safe ancestor of localPath and
// returns the root together with the clean slash-separated relative path inside
// that root. If localPath is an existing plain directory (or an OS top-level
// directory symlink such as /tmp on macOS) and asChildName != "", the target
// lands inside localPath/<asChildName>. If localPath is a relative path under
// the current directory, the root is the current directory itself so every
// intermediate component is checked for symlinks inside os.Root.
func openRootForTarget(localPath, asChildName string) (root *os.Root, baseRel string, err error) {
	cleaned := filepath.Clean(localPath)
	if !filepath.IsAbs(cleaned) && cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return openRootRelative(cleaned, asChildName)
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s: %w", localPath, err)
	}
	return openRootAbsolute(abs, localPath, asChildName)
}

func openRootRelative(cleaned, asChildName string) (*os.Root, string, error) {
	cwd, cerr := os.Getwd()
	if cerr != nil {
		return nil, "", fmt.Errorf("read current folder: %w", cerr)
	}
	r, rerr := os.OpenRoot(cwd)
	if rerr != nil {
		return nil, "", fmt.Errorf("open current folder: %w", rerr)
	}
	if cleaned == "." {
		return r, asChildName, nil
	}
	rel := filepath.ToSlash(cleaned)
	if err := ensureNoSymlinkAncestors(r, rel); err != nil {
		_ = r.Close()
		return nil, "", err
	}
	if st, lerr := r.Lstat(rel); lerr == nil {
		if st.Mode()&fs.ModeSymlink != 0 {
			return r, rel, nil
		}
		if st.IsDir() && asChildName != "" {
			return r, rel + "/" + asChildName, nil
		}
	}
	return r, rel, nil
}

func openRootAbsolute(abs, localPath, asChildName string) (*os.Root, string, error) {
	if asChildName != "" {
		if r, ok, err := openExistingDirRoot(abs, localPath); err != nil {
			return nil, "", err
		} else if ok {
			return r, asChildName, nil
		}
	}
	return openParentDirRoot(abs, localPath)
}

func openExistingDirRoot(abs, localPath string) (*os.Root, bool, error) {
	dir := ""
	if st, lerr := os.Lstat(abs); lerr == nil {
		switch {
		case st.Mode()&fs.ModeSymlink == 0 && st.IsDir():
			dir = abs
		case st.Mode()&fs.ModeSymlink != 0:
			realDir, ok, err := resolveSystemDir(abs)
			if err != nil {
				return nil, false, err
			}
			if ok {
				dir = realDir
			}
		}
	}
	if dir == "" {
		return nil, false, nil
	}
	if err := checkNoGitSegment(dir, localPath); err != nil {
		return nil, false, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, fmt.Errorf("open destination folder %s: %w", localPath, err)
	}
	return r, true, nil
}

func openParentDirRoot(abs, localPath string) (*os.Root, string, error) {
	parent := filepath.Dir(abs)
	name := filepath.Base(abs)
	st, lerr := os.Lstat(parent)
	if lerr != nil {
		return nil, "", &UserError{
			Msg:  fmt.Sprintf("destination folder %s does not exist", CleanPath(parent)),
			Hint: "Create the folder first, then run cp again.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	openDir := parent
	if st.Mode()&fs.ModeSymlink != 0 {
		realDir, ok, err := resolveSystemDir(parent)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, "", &UserError{
				Msg:  fmt.Sprintf("refused destination %s: %s is a symbolic link", CleanPath(localPath), CleanPath(parent)),
				Hint: "Copy into a plain folder without symbolic links.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
		openDir = realDir
	}
	if err := checkNoGitSegment(parent, localPath); err != nil {
		return nil, "", err
	}
	if err := checkNoGitSegment(openDir, localPath); err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(openDir)
	if err != nil {
		return nil, "", fmt.Errorf("open destination folder %s: %w", openDir, err)
	}
	return r, name, nil
}

type stagedTemp struct {
	tmpRel  string
	written int64
	preview []byte
	hasCR   bool
}

// writeTempInRoot writes up to maxBytes from src into a new unique temp file in
// the same folder as destRel inside root, mode 0644. It returns the temp file's
// relative path inside root, the bytes written, the first preview bytes for
// content review (up to previewCap), and whether any byte in the entire stream
// was a carriage return ('\r').
func writeTempInRoot(root *os.Root, destRel string, src io.Reader, fileCap, remainingCap, previewCap int64, createdDirs *[]string) (stagedTemp, error) {
	dir := path.Dir(destRel)
	if dir != "." {
		if err := mkdirAllNoSymlink(root, dir, createdDirs); err != nil {
			return stagedTemp{}, err
		}
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmpName := ".cp-tmp-" + hex.EncodeToString(rnd[:])
	tmpRel := tmpName
	if dir != "." {
		tmpRel = dir + "/" + tmpName
	}
	f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return stagedTemp{}, fmt.Errorf("create temp file for %s: %w", destRel, err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = root.Remove(tmpRel)
	}
	limit := min(fileCap, remainingCap)
	buf := make([]byte, 32<<10)
	res := stagedTemp{tmpRel: tmpRel}
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			res.written += int64(n)
			if res.written > limit {
				cleanup()
				return res, errCopyTooBig
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				cleanup()
				return res, fmt.Errorf("write temp file for %s: %w", destRel, werr)
			}
			if !res.hasCR && bytes.IndexByte(buf[:n], '\r') >= 0 {
				res.hasCR = true
			}
			if int64(len(res.preview)) < previewCap {
				take := min(int64(n), previewCap-int64(len(res.preview)))
				res.preview = append(res.preview, buf[:take]...)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			cleanup()
			return res, fmt.Errorf("read source for %s: %w", destRel, rerr)
		}
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return res, fmt.Errorf("close temp file for %s: %w", destRel, err)
	}
	return res, nil
}

// commitTempInRoot moves tmpRel to destRel inside root. Without force, it links
// and removes the temp file so a file that appeared concurrently is never
// replaced. With force, it renames, replacing a symlink without following it.
func commitTempInRoot(root *os.Root, tmpRel, destRel string, force bool) error {
	if !force {
		if err := root.Link(tmpRel, destRel); err != nil {
			_ = root.Remove(tmpRel)
			if errors.Is(err, fs.ErrExist) {
				return &UserError{
					Msg:  CleanPath(destRel) + " already exists on your laptop",
					Hint: "Pass -f to replace existing files.",
					Code: "cp_exists",
				}
			}
			return fmt.Errorf("commit %s: %w", destRel, err)
		}
		_ = root.Remove(tmpRel)
		return nil
	}
	if err := root.Rename(tmpRel, destRel); err != nil {
		_ = root.Remove(tmpRel)
		return fmt.Errorf("replace %s: %w", destRel, err)
	}
	return nil
}

var errCopyTooBig = errors.New("copy exceeded size cap")
