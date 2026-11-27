package files

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveGrant chooses the canonical containing directory as this grant's
// root and opens the named folder beneath it. A contained ordinary alias is
// allowed. A symlink escape is refused by os.Root before canonicalisation.
// The final descriptor's identity is retained for the driver's later pin.
func ResolveGrant(m Mount) (Mount, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(m.Host))
	if err != nil {
		return m, mountError("bad_mount", "the mount's containing folder cannot be resolved")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return m, err
	}
	defer root.Close()
	source, err := root.OpenRoot(filepath.Base(m.Host))
	if err != nil {
		return m, mountError("bad_mount", "the mount source leaves its containing folder or cannot be opened")
	}
	defer source.Close()
	chosen, err := source.Open(".")
	if err != nil {
		return m, err
	}
	defer chosen.Close()
	resolved, err := filepath.EvalSymlinks(filepath.Join(parent, filepath.Base(m.Host)))
	if err != nil {
		return m, err
	}
	pinned, err := PinMount(resolved, m.Write)
	if err != nil {
		return m, err
	}
	defer pinned.Close()
	want, e := chosen.Stat()
	got, e2 := pinned.Stat()
	if e != nil || e2 != nil || !os.SameFile(want, got) {
		return m, mountError("bad_mount", "the mount source changed while opening")
	}
	identity, err := Identity(pinned)
	if err != nil {
		return m, err
	}
	m.Host = resolved
	m.Identity = identity
	return m, nil
}

// PinGrant pins the canonical source and checks its original identity.
func PinGrant(m Mount) (*os.File, error) {
	if m.Identity == "" {
		return nil, fmt.Errorf("mount grant has no pinned source identity")
	}
	f, err := PinMount(m.Host, m.Write)
	if err != nil {
		return nil, err
	}
	identity, err := Identity(f)
	if err != nil || identity != m.Identity {
		f.Close()
		return nil, mountError("bad_mount", "the mount source changed after the grant")
	}
	return f, nil
}
