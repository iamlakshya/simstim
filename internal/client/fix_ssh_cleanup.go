package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/diag"
)

type ownedSSHConfig struct {
	path, owner string
	info        os.FileInfo
}

func savedSSHConfigPaths(hosts []Host) []string {
	var refs []string
	for _, h := range hosts {
		refs = append(refs, hostWrapperPaths(h)...)
		if h.SSHConfig != "" {
			refs = append(refs, filepath.Clean(expand(h.SSHConfig)))
		}
	}
	return refs
}

func savedSSHConfigReference(path string, info os.FileInfo, refs []string) bool {
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if filepath.Clean(ref) == filepath.Clean(path) {
			return true
		}
		// Existing aliases, including case-insensitive paths and symlinks,
		// protect the same file even when their spelling differs.
		if current, err := os.Stat(ref); err == nil && os.SameFile(info, current) {
			return true
		}
	}
	return false
}

func snapshotGeneratedSSHConfig(path string) (ownedSSHConfig, bool) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return ownedSSHConfig{}, false
	}
	beforeSSHConfigSnapshotOpen(path)
	f, err := diag.OpenPlain(path)
	if err != nil {
		return ownedSSHConfig{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return ownedSSHConfig{}, false
	}
	owner, ok := generatedConfigOwner(f)
	if !ok || !ValidHostName(owner) {
		return ownedSSHConfig{}, false
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return ownedSSHConfig{}, false
	}
	return ownedSSHConfig{path: path, owner: owner, info: info}, true
}

func removeOrphanSSHConfigs(ctx context.Context, candidates []ownedSSHConfig) error {
	for _, candidate := range candidates {
		if err := removeOrphanSSHConfig(ctx, candidate); err != nil {
			return err
		}
	}
	return nil
}

func removeOrphanSSHConfig(ctx context.Context, candidate ownedSSHConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The same pin then hosts order as canonical command snapshots and
	// saved host writes. No earlier lock is acquired while either is held.
	unlockPin, err := lockPin(ctx, candidate.owner)
	if err != nil {
		return err
	}
	defer unlockPin()
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	return unlinkOwnedSSHConfig(ctx, candidate, hosts)
}

// unlinkOwnedSSHConfig performs no lock acquisition. Its caller keeps the
// canonical pin and hosts locks around this exact current saved snapshot.
func unlinkOwnedSSHConfig(ctx context.Context, candidate ownedSSHConfig, hosts []Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, ok := snapshotGeneratedSSHConfig(candidate.path)
	if !ok || current.owner != candidate.owner || !os.SameFile(candidate.info, current.info) ||
		savedSSHConfigReference(candidate.path, current.info, savedSSHConfigPaths(hosts)) {
		return nil
	}
	root, err := os.OpenRoot(filepath.Dir(candidate.path))
	if err != nil {
		return fmt.Errorf("open the generated SSH config folder: %w", err)
	}
	defer root.Close()
	base := filepath.Base(candidate.path)
	info, err := root.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check the generated SSH config: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(current.info, info) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove the unreferenced generated SSH config: %w", err)
	}
	reportLocalRemoval(ctx, candidate.path)
	return nil
}

// beforeSSHConfigSnapshotOpen places a test replacement before the safe open.
var beforeSSHConfigSnapshotOpen = func(string) {}
