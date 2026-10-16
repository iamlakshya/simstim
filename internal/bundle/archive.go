package bundle

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// archive is the bundle's file: a gzipped tar with mode 0600, whose name
// holds no host name. Each file in it has mode 0600 and each folder 0700,
// so they stay private when unpacked. The headers name no user. It is
// written as <name>.part, and takes its name only once it is complete, so
// a bundle that stops leaves no file that looks whole.
type archive struct {
	path  string // the final name
	part  string // the name while it is written
	f     *os.File
	gz    *gzip.Writer
	tw    *tar.Writer
	now   time.Time
	dirs  map[string]bool
	files []File
}

// newArchive creates <name>-debug-<time>.tar.gz.part in dir.
func newArchive(dir string, now time.Time) (*archive, error) {
	p := diag.FreePath(dir, brand.Name+"-debug-"+diag.TimeName(now), ".tar.gz")
	part := p + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create the bundle: %w", err)
	}
	gz := gzip.NewWriter(f)
	return &archive{path: p, part: part, f: f, gz: gz, tw: tar.NewWriter(gz), now: now, dirs: map[string]bool{}}, nil
}

// entryName is a name that an entry of the archive may have: a clean path
// of printable ASCII, inside the archive.
var entryName = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*$`)

// checkEntryName refuses a name that could leave the folder where the
// archive is unpacked, or put a terminal's control codes on the screen.
func checkEntryName(name string) error {
	bad := !entryName.MatchString(name) || path.Clean(name) != name
	for _, part := range strings.Split(name, "/") {
		bad = bad || part == "." || part == ".."
	}
	if bad {
		return fmt.Errorf("the name %q is not a plain path inside the bundle", name)
	}
	return nil
}

// add writes one file, after the folders above it.
func (a *archive) add(name string, data []byte) error {
	if err := checkEntryName(name); err != nil {
		return err
	}
	if err := a.dir(path.Dir(name)); err != nil {
		return err
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: a.now}
	if err := a.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s into the bundle: %w", name, err)
	}
	if _, err := a.tw.Write(data); err != nil {
		return fmt.Errorf("write %s into the bundle: %w", name, err)
	}
	a.files = append(a.files, File{Name: name, Size: int64(len(data))})
	return nil
}

// dir writes a folder's entry, once, after its parents'.
func (a *archive) dir(d string) error {
	if d == "." || d == "/" || a.dirs[d] {
		return nil
	}
	if err := a.dir(path.Dir(d)); err != nil {
		return err
	}
	a.dirs[d] = true
	hdr := &tar.Header{Typeflag: tar.TypeDir, Name: strings.TrimSuffix(d, "/") + "/", Mode: 0o700, ModTime: a.now}
	if err := a.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s into the bundle: %w", d, err)
	}
	return nil
}

// close ends the archive, gives it its name, and returns its size.
func (a *archive) close() (int64, error) {
	err := errors.Join(a.tw.Close(), a.gz.Close(), a.f.Sync(), a.f.Close())
	if err == nil {
		if _, serr := os.Lstat(a.path); serr == nil {
			err = fmt.Errorf("%s appeared while the bundle was written", a.path)
		}
	}
	if err == nil {
		err = os.Rename(a.part, a.path)
	}
	if err != nil {
		_ = os.Remove(a.part)
		return 0, fmt.Errorf("write the bundle: %w", err)
	}
	st, err := os.Stat(a.path)
	if err != nil {
		return 0, fmt.Errorf("write the bundle: %w", err)
	}
	return st.Size(), nil
}

// abort removes an archive that could not be finished.
func (a *archive) abort() {
	_ = a.f.Close()
	_ = os.Remove(a.part)
}
