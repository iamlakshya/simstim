//go:build linux || darwin

package gitsource

import (
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestRootOwnedVMWorktreesAndMetadataStayOutsideTrustedSources(t *testing.T) {
	private := filepath.Join("/var/lib", brand.Name, "vms")
	var roots []sourceRoot
	for _, restriction := range PrivateRestrictions() {
		roots = append(roots, sourceRoot{name: restriction.Path, identity: restriction.Identity, direct: restriction.Direct})
	}
	for _, name := range []string{private, filepath.Join(private, "abc234de", "work"), filepath.Join(private, "abc234de", "work", ".git", "config"), filepath.Join(private, "abc234de", "work", ".git", "objects")} {
		if err := checkSourcePath(roots, name, false); err == nil {
			t.Errorf("sandbox-writable source accepted: %s", name)
		}
	}
	for _, name := range []string{filepath.Join(t.TempDir(), "ordinary-repository"), private + "-outside", filepath.Join("/var/lib", brand.Name, "store")} {
		if err := checkSourcePath(roots, name, false); err != nil {
			t.Errorf("the slot refusal changed an outside path: %s: %v", name, err)
		}
	}
}
