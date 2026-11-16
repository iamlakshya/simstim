package host

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// eventTargets enumerates lookup hints only. A directory never becomes an
// event, readiness fact or permission to start/restore anything.
func eventTargets(ctx context.Context, target string) ([]string, error) {
	if target != "" {
		return []string{target}, nil
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	var ids []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := directory.ReadDir(256)
		for _, entry := range entries {
			if entry.IsDir() && api.ValidSessionID(entry.Name()) {
				ids = append(ids, entry.Name())
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func eventPrimaryHint(id string) string {
	manifest, err := LoadManifest(id)
	if err == nil && manifest.ID == id && api.ValidSessionID(manifest.SandboxOf) {
		return manifest.SandboxOf
	}
	return id
}
