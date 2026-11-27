package gitsource

import (
	"context"
	"path/filepath"
	"unicode/utf8"
)

// CheckDestinations adds only refusals for explicit local copy targets. The
// actual filecopy owner separately pins/no-follows every filesystem component;
// no nonexistent destination or caller Boolean becomes source/read authority.
// All targets share this SAME canonical complete inventory and path grammar.
func (c *Capability) CheckDestinations(ctx context.Context, names []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return sourceRefused()
	}
	authority := c.authority.clone()
	authority.ctx = ctx
	roots, err := authority.writeRoots()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !filepath.IsAbs(name) || filepath.Clean(name) != name || !utf8.ValidString(name) {
			return metadataRefused()
		}
		if err := validateSourceSpelling(name); err != nil {
			return err
		}
		if err := checkSourcePath(roots, name, false); err != nil {
			return err
		}
	}
	return ctx.Err()
}
