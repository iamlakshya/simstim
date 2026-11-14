package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// GitSourceRestriction adds a refusal for an actual locally inspected writable
// provider root. It grants no filesystem access, ownership or authorisation.
// Identity is the canonical files.Identity of its held opened-object pin.
type GitSourceRestriction struct {
	Path     string
	Identity string
}

// sourceAuthority always retains the canonical host manifest/private inventory.
// Its optional provider facts only add restrictions. This is an operation
// dependency, never a context flag, process registry or replacement parser.
type sourceAuthority struct {
	extra []GitSourceRestriction
}

func (a sourceAuthority) writeRoots() ([]sourceRoot, error) {
	roots, err := sourceWriteRoots()
	if err != nil {
		return nil, err
	}
	for _, root := range a.extra {
		if !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || !utf8.ValidString(root.Path) ||
			strings.ContainsAny(root.Path, "\x00\r\n") || root.Identity == "" {
			return nil, UserError("seed_source", "a local provider writable root has incomplete source provenance", "Read the provider's current local configuration before running local Git.")
		}
		resolved, err := filepath.EvalSymlinks(root.Path)
		if err != nil {
			return nil, UserError("seed_source", "a local provider writable root cannot be checked", "Repair the provider's local sharing configuration before running local Git.")
		}
		roots = append(roots, sourceRoot{name: root.Path, identity: root.Identity, direct: true})
		if resolved != root.Path {
			roots = append(roots, sourceRoot{name: resolved, identity: root.Identity, direct: true})
		}
	}
	return roots, nil
}

func (a sourceAuthority) clone() sourceAuthority {
	return sourceAuthority{extra: slices.Clone(a.extra)}
}

func (a sourceAuthority) checkMetadataIdentity(file *os.File) error {
	roots, err := a.writeRoots()
	if err != nil {
		return err
	}
	return checkSourceIdentity(roots, file)
}

func (a sourceAuthority) trustedSource(name string) error {
	pin, err := a.pinTrustedSource(name, true)
	if err == nil {
		pin.close()
	}
	return err
}
