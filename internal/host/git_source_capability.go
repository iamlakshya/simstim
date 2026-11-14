package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/files"
)

// GitSourceCapability is a read-only opaque view of the canonical host source
// guard. It invokes no process, jail, namespace operation or alternate parser.
// Held directory/file descriptors retain inspected objects until Close.
type GitSourceCapability struct {
	mu        sync.Mutex
	path      string
	graph     bool
	identity  os.FileInfo
	pins      []*sourcePin
	files     []*os.File
	closed    bool
	authority sourceAuthority
}

// OpenGitSourceCapability uses the SAME full LoadManifest/write-root inventory,
// link-sensitive walker and metadata/commondir/alternate policy as host Git.
// Graph=false is only beneath-source discovery before a repository is found.
// A real repository operation requires Graph=true before native Git starts.
func OpenGitSourceCapability(ctx context.Context, name string, graph bool) (_ *GitSourceCapability, err error) {
	return OpenGitSourceCapabilityRestricted(ctx, name, graph)
}

// OpenGitSourceCapabilityRestricted adds actual inspected provider refusals to
// the SAME canonical manifest/private source guard. Restrictions never replace
// that inventory or authorise a read. Their actual inspector owns completeness
// and must revalidate its current configuration before native launch.
func OpenGitSourceCapabilityRestricted(ctx context.Context, name string, graph bool, restrictions ...GitSourceRestriction) (_ *GitSourceCapability, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	authority := (sourceAuthority{extra: restrictions}).clone()
	pin, err := authority.pinTrustedSource(name, graph)
	if err != nil {
		return nil, err
	}
	capability := &GitSourceCapability{path: pin.name, graph: graph, pins: []*sourcePin{pin}, authority: authority}
	defer func() {
		if err != nil {
			_ = capability.Close()
		}
	}()
	root := pin.roots[len(pin.roots)-1]
	capability.identity, err = root.Stat(".")
	if err != nil {
		return nil, err
	}
	if graph {
		repository, metadata, err := authority.seedRoot(pin.name)
		if err != nil {
			return nil, err
		}
		if repository != "" {
			repoPin, err := authority.pinTrustedSource(repository, true)
			if err != nil {
				return nil, err
			}
			capability.pins = append(capability.pins, repoPin)
		}
		for _, name := range metadata {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := os.Lstat(name)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				metaPin, err := authority.pinTrustedSource(name, true)
				if err != nil {
					return nil, err
				}
				capability.pins = append(capability.pins, metaPin)
				continue
			}
			parent, err := authority.pinTrustedSource(filepath.Dir(name), true)
			if err != nil {
				return nil, err
			}
			capability.pins = append(capability.pins, parent)
			file, err := files.OpenContainedRegular(parent.roots[len(parent.roots)-1], filepath.Base(name))
			if err != nil {
				return nil, err
			}
			actual, err := file.Stat()
			if err != nil || !os.SameFile(info, actual) {
				file.Close()
				return nil, sourceRefused()
			}
			err = authority.checkMetadataIdentity(file)
			if err != nil {
				file.Close()
				return nil, err
			}
			capability.files = append(capability.files, file)
		}
	}
	return capability, ctx.Err()
}

// Path is the canonical checked source spelling. Native execution must use it,
// never the unchecked input spelling that produced this capability.
func (c *GitSourceCapability) Path() string { return c.path }

// Revalidate checks fresh inventory and the same source identity. This is a
// fail-closed check, not a claim of atomicity with a concurrent grant or ambient
// pathname use by an external native tool. Those uncertainties remain explicit.
func (c *GitSourceCapability) Revalidate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return sourceRefused()
	}
	fresh, err := OpenGitSourceCapabilityRestricted(ctx, c.path, c.graph, c.authority.extra...)
	if err != nil {
		return err
	}
	defer fresh.Close()
	if !os.SameFile(c.identity, fresh.identity) {
		return sourceRefused()
	}
	return ctx.Err()
}

// Close releases only the read-only descriptors of this capability.
func (c *GitSourceCapability) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var failures []error
	for _, file := range c.files {
		failures = append(failures, file.Close())
	}
	for index := len(c.pins) - 1; index >= 0; index-- {
		pin := c.pins[index]
		for inner := len(pin.roots) - 1; inner >= 0; inner-- {
			failures = append(failures, pin.roots[inner].Close())
		}
		pin.roots = nil
	}
	return errors.Join(failures...)
}
