//go:build linux

package process

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

//go:embed etc.toml
var etcFiles embed.FS

// MountPlan lists only the system entries, helper, explicit grants and kept
// state that this sandbox needs. The caller's environment is never mounted.
func MountPlan(spec wall.Spec) ([]wall.Mount, error) {
	if err := (files.StatePlan{Dirs: spec.StateDirs, Files: spec.StateFiles}).Validate(); err != nil {
		return nil, fmt.Errorf("validate kept state: %w", err)
	}
	canonicalSession, err := resolveSessionDir(spec.SessionDir)
	if err != nil {
		return nil, err
	}
	var plan []wall.Mount
	add := func(host, guest string, write, optional bool) error {
		resolved, err := filepath.EvalSymlinks(host)
		if err != nil {
			if optional && os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("resolve mount source: %w", err)
		}
		if !validGuest(guest) {
			return fmt.Errorf("unsafe guest mount %q", guest)
		}
		for _, old := range plan {
			if old.Guest == guest || strings.HasPrefix(guest, old.Guest+"/") {
				if !write && !old.Write {
					return nil
				}
				return fmt.Errorf("overlapping writable mount %q", guest)
			}
		}
		plan = append(plan, wall.Mount{Host: resolved, Guest: guest, Write: write})
		return nil
	}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if err := add(p, p, false, true); err != nil {
			return nil, err
		}
	}
	var entries struct {
		Entries []string `toml:"entries"`
	}
	b, _ := etcFiles.ReadFile("etc.toml")
	if _, err := toml.Decode(string(b), &entries); err != nil {
		return nil, fmt.Errorf("read filtered etc plan: %w", err)
	}
	for _, name := range entries.Entries {
		if name == "" || path.Base(name) != name {
			return nil, fmt.Errorf("invalid filtered etc entry")
		}
		if err := add("/etc/"+name, "/etc/"+name, false, true); err != nil {
			return nil, err
		}
	}
	if err := add(spec.Helper, "/opt/"+brand.Name+"/bin", false, false); err != nil {
		return nil, err
	}
	for _, m := range spec.Mounts {
		if !filepath.IsAbs(m.Host) || filepath.Clean(m.Host) != m.Host || !validGuest(m.Guest) {
			return nil, fmt.Errorf("mount paths must be canonical absolute paths")
		}
		// Grants deliberately do not resolve symlinks. pinSource below refuses
		// every link component, including a source exchanged after this check.
		for _, old := range plan {
			if m.Guest == old.Guest || strings.HasPrefix(m.Guest, old.Guest+"/") {
				return nil, fmt.Errorf("grant overlaps system mount")
			}
		}
		plan = append(plan, m)
	}
	session, err := os.OpenRoot(spec.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("open session state root: %w", err)
	}
	defer session.Close()
	for _, state := range spec.StateDirs {
		if path.IsAbs(state) || path.Clean(state) != state || state == "." || state == ".." || strings.HasPrefix(state, "../") {
			return nil, fmt.Errorf("state folder must stay beneath the agent home")
		}
		host := filepath.Join(canonicalSession, "state", filepath.FromSlash(state))
		if err := session.MkdirAll(filepath.Join("state", filepath.FromSlash(state)), 0700); err != nil {
			return nil, fmt.Errorf("make kept agent state: %w", err)
		}
		plan = append(plan, wall.Mount{Host: host, Guest: "/home/agent/" + state, Write: true})
	}
	if len(spec.StateFiles) > 0 {
		if err := files.ValidateStateMounts(spec.SessionDir, spec.StateRestore, spec.StateCapture); err != nil {
			return nil, fmt.Errorf("validate state generation mounts: %w", err)
		}
		for _, mount := range []wall.Mount{{Host: spec.StateRestore, Guest: files.RestorePath}, {Host: spec.StateCapture, Guest: files.CapturePath, Write: true}} {
			mount.Host, err = filepath.EvalSymlinks(mount.Host)
			if err != nil {
				return nil, fmt.Errorf("resolve state generation source: %w", err)
			}
			plan = append(plan, mount)
		}
	} else if spec.StateRestore != "" || spec.StateCapture != "" {
		return nil, fmt.Errorf("state generations need a validated file plan")
	}
	slices.SortStableFunc(plan, func(a, b wall.Mount) int { return strings.Compare(a.Guest, b.Guest) })
	for i, m := range plan {
		for _, old := range plan[:i] {
			if m.Guest == old.Guest || strings.HasPrefix(m.Guest, old.Guest+"/") {
				return nil, fmt.Errorf("mount destinations overlap")
			}
		}
	}
	return plan, nil
}

func pinSource(name string) (*os.File, error) {
	root, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open source root: %w", err)
	}
	defer unix.Close(root)
	fd, err := unix.Openat2(root, strings.TrimPrefix(name, "/"), &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, fmt.Errorf("pin mount source without links: %w", err)
	}
	f := os.NewFile(uintptr(fd), "mount-source")
	st, err := f.Stat()
	if err != nil || !(st.IsDir() || st.Mode().IsRegular()) {
		f.Close()
		return nil, fmt.Errorf("mount source is not a regular file or directory")
	}
	return f, nil
}
