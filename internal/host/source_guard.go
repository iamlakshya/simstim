package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/gitsource"
	"github.com/iamlakshya/simstim/internal/paths"
)

type sourceRoot struct {
	name, identity string
	direct         bool
}

func sourceWriteRoots() ([]sourceRoot, error) {
	var out []sourceRoot
	add := func(name, identity string, direct bool) {
		out = append(out, sourceRoot{name: name, identity: identity, direct: direct})
		if resolved, err := filepath.EvalSymlinks(name); err == nil && resolved != name {
			out = append(out, sourceRoot{name: resolved, identity: identity, direct: direct})
		}
	}
	for _, r := range gitsource.PrivateRestrictions() {
		add(r.Path, r.Identity, r.Direct)
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

func hostSourceInventory(context.Context) ([]gitsource.Restriction, error) {
	roots, err := sourceWriteRoots()
	if err != nil {
		return nil, err
	}
	out := make([]gitsource.Restriction, 0, len(roots))
	for _, r := range roots {
		out = append(out, gitsource.Restriction{Path: r.name, Identity: r.identity, Direct: r.direct})
	}
	return out, nil
}

type sourcePin struct {
	pin  *gitsource.Pin
	name string
}

func (p *sourcePin) close() { _ = p.pin.Close() }
func pinTrustedSource(name string, children bool) (*sourcePin, error) {
	p, err := gitsource.PinPath(gitContext(), name, children, hostSourceInventory)
	if err != nil {
		return nil, err
	}
	return &sourcePin{pin: p, name: p.Path()}, nil
}
func trustedSource(name string) error {
	p, err := pinTrustedSource(name, true)
	if err == nil {
		p.close()
	}
	return err
}
func sourceRefused() error {
	return UserError("seed_source", "this folder overlaps files that a sandbox can write", "Pull the work into a separate trusted repository first.")
}
