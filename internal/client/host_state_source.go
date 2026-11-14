package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// An absent source is valid only before a registration/publication observes
// anything. Canonical writers never delete hosts.toml, so its first actual
// publication permanently invalidates this absent source. Absence grants no
// forget or retirement authority.
type hostStateSource struct {
	exists     bool
	generation string
	root       cachefs.Reference
	path       string
}

func captureHostStateSource(ctx context.Context, hosts []Host, migrate bool) (hostStateSource, error) {
	var source hostStateSource
	if err := requireHostMembershipContext(ctx); err != nil {
		return source, err
	}
	path := hostsPath()
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if err != nil {
		return source, err
	}
	defer root.Close()
	reference, err := cachefs.CaptureRoot(root)
	if err != nil {
		return source, err
	}
	source = hostStateSource{path: path, root: reference}
	file, err := cachefs.OpenFile(root, filepath.Base(path), os.O_RDONLY, 0600)
	if errors.Is(err, os.ErrNotExist) {
		if len(hosts) != 0 {
			return source, hostStateRemovalError("saved hosts disappeared before the observation")
		}
		return source, source.check(ctx)
	}
	if err != nil {
		return source, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	fileReference, err := cachefs.CaptureFile(root, filepath.Base(path), file, 0600)
	if err != nil {
		return source, err
	}
	generation, err := readMembershipGeneration(ctx, file)
	if err != nil {
		if !migrate || !errors.Is(err, errMembershipGenerationMissing) {
			return source, err
		}
		// Existing valid legacy TOML may be migrated. A malformed or newer
		// member of this marker family is preserved, never rewritten as old.
		var prefix [len("# membership-generation")]byte
		n, readErr := file.ReadAt(prefix[:], 0)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return source, readErr
		}
		if bytes.HasPrefix(prefix[:n], []byte("# membership-generation")) {
			return source, hostStateRemovalError("the membership generation marker is malformed or newer")
		}
		if err := fileReference.CheckFile(root, filepath.Base(path), file, 0600); err != nil {
			return source, err
		}
		if err := file.Close(); err != nil {
			closed = true
			return source, err
		}
		closed = true
		generation, err = newMembershipGeneration()
		if err != nil {
			return source, err
		}
		if err := ctx.Err(); err != nil {
			return source, err
		}
		if err := writeHostsSelectedGeneration(hosts, generation); err != nil {
			return source, err
		}
	} else if err := fileReference.CheckFile(root, filepath.Base(path), file, 0600); err != nil {
		return source, err
	}
	source.exists, source.generation = true, generation
	return source, source.check(ctx)
}

func (s hostStateSource) check(ctx context.Context) error {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if s.path == "" || s.path != hostsPath() {
		return hostStateRemovalError("the observed membership location changed")
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(s.path), false)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.root.CheckRoot(root); err != nil {
		return err
	}
	if !s.exists {
		if s.generation != "" {
			return hostStateRemovalError("an absent membership source has a fabricated generation")
		}
		if _, err := root.Lstat(filepath.Base(s.path)); !errors.Is(err, os.ErrNotExist) {
			return hostStateRemovalError("the original absent membership source was published")
		}
		return ctx.Err()
	}
	if !ValidHostID(s.generation) {
		return hostStateRemovalError("the observed membership source has no canonical generation")
	}
	current, err := openHostMembershipSnapshot(ctx)
	if err != nil {
		return err
	}
	defer current.close()
	if current.generation != s.generation {
		return hostStateRemovalError("saved hosts changed after the original observation")
	}
	if err := s.root.CheckRoot(current.root); err != nil {
		return err
	}
	return current.check(ctx)
}
