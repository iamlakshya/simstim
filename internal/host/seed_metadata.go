package host

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/files"
)

// These metadata bounds are project limits, not claims about Git's limits.
const seedMetadataBytes = 64 << 10
const seedObjectRoots = 1000

func metadataRefused() error {
	return UserError("seed_source", "this repository needs Git metadata that cannot be safely projected", "Make a self-contained clone with local Git metadata, then use that clone as the source.")
}

func metadataLine(data []byte) (string, error) {
	value := strings.TrimSuffix(string(data), "\n")
	if !utf8.ValidString(value) || value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "\"") {
		return "", metadataRefused()
	}
	return value, nil
}

func bareMetadata(root *os.Root) bool {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		st, err := root.Lstat(name)
		if err != nil {
			return false
		}
		if name == "HEAD" && st.IsDir() {
			return false
		}
		if name != "HEAD" && !(st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
			return false
		}
	}
	return true
}

func (a sourceAuthority) metadataScope(name string) error {
	home, err := os.UserHomeDir()
	if err == nil {
		if canonical, e := filepath.EvalSymlinks(home); e == nil {
			home = canonical
		}
		if sourceBeneath(home, name) {
			return metadataRefused()
		}
	}
	roots, err := a.writeRoots()
	if err != nil {
		return err
	}
	for _, root := range roots {
		if sourceBeneath(name, root.name) || sourceBeneath(root.name, name) {
			return metadataRefused()
		}
	}
	return nil
}

type seedView struct {
	authority sourceAuthority
	source    string
	paths     []string
	objects   []string
	seen      map[string]bool
}

func (v *seedView) add(name string) {
	for _, old := range v.paths {
		if old == name {
			return
		}
	}
	v.paths = append(v.paths, name)
}

func (v *seedView) gitDirectory(dir string) error {
	if err := v.authority.metadataScope(dir); err != nil {
		return err
	}
	if dir != v.source && sourceBeneath(v.source, dir) {
		return metadataRefused()
	}
	v.add(dir)
	opened, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer opened.Close()
	for _, name := range []string{"config", "config.worktree", "HEAD", "refs", "packed-refs", "commondir"} {
		st, err := opened.Lstat(name)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			if name == "refs" {
				contained, err := opened.OpenRoot(name)
				if err != nil {
					return metadataRefused()
				}
				contained.Close()
			} else {
				contained, err := files.OpenContainedRegular(opened, name)
				if err != nil {
					return metadataRefused()
				}
				contained.Close()
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := opened.Lstat("objects"); err == nil {
		v.objects = append(v.objects, dir+string(filepath.Separator)+"objects")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (v *seedView) alternates(directory *os.Root, name string) error {
	if _, err := directory.Lstat("info/http-alternates"); err == nil {
		return metadataRefused()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := files.OpenContainedRegular(directory, "info/alternates")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return metadataRefused()
	}
	if err := v.authority.checkMetadataIdentity(file); err != nil {
		file.Close()
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, seedMetadataBytes+1))
	file.Close()
	if err != nil || len(data) > seedMetadataBytes {
		return metadataRefused()
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		alternate, err := metadataLine([]byte(line))
		if err != nil {
			return err
		}
		if !filepath.IsAbs(alternate) {
			alternate = name + string(filepath.Separator) + alternate
		}
		v.objects = append(v.objects, alternate)
		if len(v.objects)+len(v.seen) > seedObjectRoots {
			return metadataRefused()
		}
	}
	return nil
}

func (v *seedView) objectRoot(name string) error {
	requested := name
	pin, err := v.authority.pinTrustedSource(name, true)
	if err != nil {
		return err
	}
	defer pin.close()
	name = pin.name
	if err = v.authority.metadataScope(name); err != nil {
		return err
	}
	if sourceBeneath(v.source, name) {
		return metadataRefused()
	}
	directory := pin.roots[len(pin.roots)-1]
	fd, err := directory.Open(".")
	if err != nil {
		return err
	}
	identity, err := files.Identity(fd)
	fd.Close()
	if err != nil {
		return err
	}
	if v.seen[identity] {
		return nil
	}
	v.seen[identity] = true
	if len(v.seen) > seedObjectRoots {
		return metadataRefused()
	}
	// Standard object databases have these directories. Other layouts fail
	// closed rather than exposing an arbitrary host folder as objects.
	for _, part := range []string{"info", "pack"} {
		st, err := directory.Lstat(part)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return metadataRefused()
		}
	}
	v.add(name)
	if filepath.Clean(requested) == requested {
		v.add(requested)
	}
	return v.alternates(directory, name)
}

// seedMetadata supplies only Git metadata. The jail synthesises its parent
// directories, so -C can name the source without exposing its work tree.
func seedMetadata(root, entry string, dirs []string) ([]string, error) {
	return (sourceAuthority{}).seedMetadata(root, entry, dirs)
}

func (a sourceAuthority) seedMetadata(root, entry string, dirs []string) ([]string, error) {
	view := seedView{authority: a, source: root, seen: map[string]bool{}}
	if entry != "" {
		view.add(entry)
	}
	for _, dir := range dirs {
		if err := view.gitDirectory(dir); err != nil {
			return nil, err
		}
	}
	for len(view.objects) > 0 {
		name := view.objects[0]
		view.objects = view.objects[1:]
		if err := view.objectRoot(name); err != nil {
			return nil, err
		}
	}
	return view.paths, nil
}
