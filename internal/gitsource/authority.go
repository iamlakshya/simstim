package gitsource

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Restriction is an additive refusal, never source permission or root ownership.
type Restriction struct {
	Path, Identity string
	Direct         bool
}

// Inventory loads the actual complete managed inventory. Unknown is an error.
type Inventory func(context.Context) ([]Restriction, error)

type sourceAuthority struct {
	ctx       context.Context
	inventory Inventory
	extra     []Restriction
}

func sourceError(message, hint string) error {
	return &proto.Error{Code: "seed_source", Message: message, Hint: hint}
}
func (a sourceAuthority) clone() sourceAuthority {
	return sourceAuthority{ctx: a.ctx, inventory: a.inventory, extra: slices.Clone(a.extra)}
}

// PrivateRestrictions is the ONE canonical known-private directory inventory.
// Missing directories remain path refusals; their absence grants nothing.
func PrivateRestrictions() []Restriction {
	var out []Restriction
	for _, name := range []string{paths.StateDir(), filepath.Dir(paths.SandboxRoot("unused"))} {
		identity := ""
		if root, err := os.OpenRoot(name); err == nil {
			if file, err := root.Open("."); err == nil {
				identity, _ = files.Identity(file)
				file.Close()
			}
			root.Close()
		}
		out = append(out, Restriction{Path: name, Identity: identity})
	}
	return out
}
func (a sourceAuthority) writeRoots() ([]sourceRoot, error) {
	if a.inventory == nil || a.ctx == nil {
		return nil, sourceError("the complete local source inventory is unknown", "Use the canonical source composition entrypoint.")
	}
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	supplied, err := a.inventory(a.ctx)
	if err != nil {
		return nil, err
	}
	restrictions := append(PrivateRestrictions(), supplied...)
	for _, r := range a.extra {
		r.Direct = true
		restrictions = append(restrictions, r)
	}
	var roots []sourceRoot
	for _, r := range restrictions {
		if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || !utf8.ValidString(r.Path) || strings.ContainsAny(r.Path, "\x00\r\n") || r.Direct && r.Identity == "" {
			return nil, sourceError("a writable source restriction has incomplete provenance", "Repair the actual source inventory before running local Git.")
		}
		roots = append(roots, sourceRoot{name: r.Path, identity: r.Identity, direct: r.Direct})
		if resolved, err := filepath.EvalSymlinks(r.Path); err == nil && resolved != r.Path {
			roots = append(roots, sourceRoot{name: resolved, identity: r.Identity, direct: r.Direct})
		} else if err != nil && r.Direct {
			return nil, sourceError("a writable source restriction cannot be checked", "Repair the actual provider configuration before running local Git.")
		}
	}
	return roots, nil
}
func (a sourceAuthority) checkMetadataIdentity(file *os.File) error {
	roots, err := a.writeRoots()
	if err != nil {
		return err
	}
	return checkSourceIdentity(roots, file)
}
func (a sourceAuthority) trustedSource(name string) error {
	p, err := a.pinTrustedSource(name, true)
	if err == nil {
		p.close()
	}
	return err
}

// Pin holds the SAME canonical walker's read-only directory descriptors.
type Pin struct{ pin *sourcePin }

// Path returns only its checked canonical spelling.
func (p *Pin) Path() string { return p.pin.name }

// Close releases this operation's read-only descriptors.
func (p *Pin) Close() error {
	if p != nil && p.pin != nil {
		p.pin.close()
		p.pin = nil
	}
	return nil
}

// PinPath uses the ONE walker with a mandatory complete inventory.
func PinPath(ctx context.Context, name string, children bool, inventory Inventory) (*Pin, error) {
	a := sourceAuthority{ctx: ctx, inventory: inventory}
	p, err := a.pinTrustedSource(name, children)
	if err != nil {
		return nil, err
	}
	return &Pin{pin: p}, nil
}

// Metadata uses the ONE native metadata/pointer/commondir/alternate parser.
func Metadata(ctx context.Context, name string, inventory Inventory) (string, []string, error) {
	return (sourceAuthority{ctx: ctx, inventory: inventory}).seedRoot(name)
}
