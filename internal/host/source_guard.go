package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/paths"
)

type sourceRoot struct {
	name     string
	identity string
	direct   bool
}

type sourcePin struct {
	name  string
	roots []*os.Root
}

func (p *sourcePin) close() {
	for i := len(p.roots) - 1; i >= 0; i-- {
		_ = p.roots[i].Close()
	}
	p.roots = nil
}

func sourceRefused() error {
	return UserError("seed_source", "this folder overlaps files that a sandbox can write", "Pull the work into a separate trusted repository first.")
}

func sourceWriteRoots() ([]sourceRoot, error) {
	var out []sourceRoot
	add := func(name, identity string, direct bool) {
		out = append(out, sourceRoot{name: name, identity: identity, direct: direct})
		if resolved, err := filepath.EvalSymlinks(name); err == nil && resolved != name {
			out = append(out, sourceRoot{name: resolved, identity: identity, direct: direct})
		}
	}
	for _, name := range []string{paths.StateDir(), filepath.Dir(paths.SandboxRoot("unused"))} {
		identity := ""
		if root, err := os.OpenRoot(name); err == nil {
			if fd, err := root.Open("."); err == nil {
				identity, _ = files.Identity(fd)
				fd.Close()
			}
			root.Close()
		}
		add(name, identity, false)
	}
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if !validID(entry.Name()) {
			continue
		}
		manifest, err := LoadManifest(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, UserError("seed_source", "the helper cannot check a session's writable folders", "Run host doctor and repair the session's manifest before running host Git.")
		}
		if manifest.Files == nil {
			continue
		}
		for _, mount := range manifest.Files.Mounts {
			if !mount.Write {
				continue
			}
			if !filepath.IsAbs(mount.Host) || filepath.Clean(mount.Host) != mount.Host || mount.Host == string(filepath.Separator) || mount.Identity == "" {
				return nil, UserError("seed_source", "a writable sandbox grant has no source identity", "Stop and recreate that sandbox with the current helper.")
			}
			add(mount.Host, mount.Identity, true)
		}
	}
	return out, nil
}

func sourceBeneath(name, root string) bool {
	if root == filepath.VolumeName(root)+string(filepath.Separator) {
		return strings.HasPrefix(name, root)
	}
	return name == root || strings.HasPrefix(name, root+string(filepath.Separator))
}

func checkSourcePath(roots []sourceRoot, name string, children bool) error {
	for _, root := range roots {
		if sourceBeneath(name, root.name) || children && root.direct && sourceBeneath(root.name, name) {
			return sourceRefused()
		}
	}
	return nil
}

func checkSourceIdentity(roots []sourceRoot, fd *os.File) error {
	identity, err := files.Identity(fd)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if root.identity != "" && identity == root.identity {
			return sourceRefused()
		}
	}
	return nil
}

type sourceWalk struct {
	pin     *sourcePin
	roots   []sourceRoot
	top     string
	pending []string
	links   int
}

func (w *sourceWalk) followLink(current *os.Root, part string, st os.FileInfo) error {
	fd, err := current.Open(".")
	if err != nil {
		return err
	}
	magic, err := files.IsProcDir(fd)
	fd.Close()
	if err != nil {
		return err
	}
	if magic {
		return UserError("seed_source", "a Git source cannot use a process magic link", "Name the repository by an ordinary folder path.")
	}
	w.links++
	if w.links > 40 {
		return fmt.Errorf("too many source symbolic links")
	}
	target, err := current.Readlink(part)
	if err != nil {
		return err
	}
	again, err := current.Lstat(part)
	if err != nil || !os.SameFile(st, again) {
		return UserError("seed_source", "a source link changed while resolving it", "Try again with a stable source folder.")
	}
	if filepath.IsAbs(target) {
		w.pin.close()
		initial, err := os.OpenRoot(w.top)
		if err != nil {
			return err
		}
		w.pin.roots = []*os.Root{initial}
		w.pin.name = w.top
		target = strings.TrimPrefix(target, w.top)
	}
	w.pending = append(strings.Split(target, string(filepath.Separator)), w.pending...)
	return nil
}

func (w *sourceWalk) enter(current *os.Root, part, candidate string, st os.FileInfo) error {
	if !st.IsDir() {
		return fmt.Errorf("source component is not a directory")
	}
	next, err := current.OpenRoot(part)
	if err != nil {
		return err
	}
	fd, err := next.Open(".")
	if err != nil {
		next.Close()
		return err
	}
	actual, err := fd.Stat()
	if err == nil && !os.SameFile(st, actual) {
		err = fmt.Errorf("source directory changed while opening")
	}
	if err == nil {
		err = checkSourceIdentity(w.roots, fd)
	}
	fd.Close()
	if err != nil {
		next.Close()
		return err
	}
	w.pin.roots = append(w.pin.roots, next)
	w.pin.name = candidate
	return nil
}

func (w *sourceWalk) step() error {
	part := w.pending[0]
	w.pending = w.pending[1:]
	switch part {
	case "", ".":
		return nil
	case "..":
		if len(w.pin.roots) > 1 {
			_ = w.pin.roots[len(w.pin.roots)-1].Close()
			w.pin.roots = w.pin.roots[:len(w.pin.roots)-1]
			w.pin.name = filepath.Dir(w.pin.name)
		}
		return nil
	}
	candidate := w.pin.name
	if candidate != w.top {
		candidate += string(filepath.Separator)
	}
	candidate += part
	if err := checkSourcePath(w.roots, candidate, false); err != nil {
		return err
	}
	current := w.pin.roots[len(w.pin.roots)-1]
	st, err := current.Lstat(part)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return w.followLink(current, part, st)
	}
	return w.enter(current, part, candidate, st)
}

// pinTrustedSource walks links itself so that entering a mutable root is
// refused before any deeper link is read. Dot-dot acts after each link's
// target, as the kernel does. Held directory roots prevent inode reuse
// during the operation. Git receives only the resulting canonical name.
func pinTrustedSource(name string, children bool) (_ *sourcePin, err error) {
	return (sourceAuthority{}).pinTrustedSource(name, children)
}

func (a sourceAuthority) pinTrustedSource(name string, children bool) (_ *sourcePin, err error) {
	if !utf8.ValidString(name) {
		return nil, metadataRefused()
	}
	roots, err := a.writeRoots()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(name) {
		cwd, e := os.Getwd()
		if e != nil {
			return nil, e
		}
		name = cwd + string(filepath.Separator) + name
	}
	top := filepath.VolumeName(name) + string(filepath.Separator)
	initial, err := os.OpenRoot(top)
	if err != nil {
		return nil, err
	}
	pin := &sourcePin{name: top, roots: []*os.Root{initial}}
	defer func() {
		if err != nil {
			pin.close()
		}
	}()
	walk := sourceWalk{pin: pin, roots: roots, top: top, pending: strings.Split(strings.TrimPrefix(name, top), string(filepath.Separator))}
	for len(walk.pending) > 0 {
		if err = walk.step(); err != nil {
			return nil, err
		}
	}
	if err = checkSourcePath(roots, pin.name, children); err != nil {
		return nil, err
	}
	return pin, nil
}

func trustedSource(name string) error {
	return (sourceAuthority{}).trustedSource(name)
}
