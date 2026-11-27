package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateStateMounts checks the two distinct storage roles against the
// session root. A committed generation is never an incoming capture area.
// Neither role may expose CURRENT or the generations' parent directory.
func ValidateStateMounts(sessionDir, restore, capture string) error {
	if !filepath.IsAbs(sessionDir) || filepath.Clean(sessionDir) != sessionDir {
		return fmt.Errorf("state session root must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return err
	}
	sessionDir = canonical
	relative := func(name string) (string, error) {
		if !filepath.IsAbs(name) || filepath.Clean(name) != name {
			return "", fmt.Errorf("state mount must be absolute and clean")
		}
		rel, err := filepath.Rel(sessionDir, name)
		if err != nil || !Relative(filepath.ToSlash(rel)) {
			return "", fmt.Errorf("state mount leaves its session root")
		}
		return filepath.ToSlash(rel), nil
	}
	old, err := relative(restore)
	if err != nil {
		return err
	}
	next, err := relative(capture)
	if err != nil {
		return err
	}
	validRestore := old == "state-files/empty"
	if name, ok := strings.CutPrefix(old, "state-files/epochs/"); ok {
		validRestore = epochName(name)
	}
	token, validCapture := strings.CutPrefix(next, "state-incoming/")
	validCapture = validCapture && epochName("e-"+token)
	if !validRestore || !validCapture || old == next {
		return fmt.Errorf("state mounts do not name a committed generation and a fresh incoming area")
	}
	root, err := os.OpenRoot(sessionDir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range []struct{ rel, absolute string }{{old, restore}, {next, capture}} {
		st, err := root.Lstat(item.rel)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("state mount is not a rooted directory")
		}
		pinned, err := PinMount(item.absolute, false)
		if err != nil {
			return err
		}
		got, err := pinned.Stat()
		pinned.Close()
		if err != nil || !os.SameFile(st, got) {
			return fmt.Errorf("state mount changed while opening")
		}
	}
	return nil
}
