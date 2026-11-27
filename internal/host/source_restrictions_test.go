//go:build linux || darwin

package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func localRestriction(t *testing.T, name string) GitSourceRestriction {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := files.Identity(file)
	if err != nil {
		t.Fatal(err)
	}
	return GitSourceRestriction{Path: name, Identity: identity}
}

func writeSourceMetadata(t *testing.T, directory string) {
	t.Helper()
	for _, name := range []string{"objects/info", "objects/pack", "refs"} {
		if err := os.MkdirAll(filepath.Join(directory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config"), []byte("[core]\nrepositoryformatversion=0\nbare=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireRestrictedSource(t *testing.T, name string, restrictions ...GitSourceRestriction) {
	t.Helper()
	capability, err := OpenGitSourceCapabilityRestricted(t.Context(), name, true, restrictions...)
	if capability != nil {
		capability.Close()
		t.Fatal("a restricted source received a capability")
	}
	var refused *proto.Error
	if !errors.As(err, &refused) || refused.Code != "seed_source" || refused.Hint == "" {
		t.Fatalf("source restriction lost its canonical refusal: %v", err)
	}
}

func TestSourceProviderRestrictionsOnlyAddToCanonicalInventory(t *testing.T) {
	root, _ := sourceGuardFixture(t)
	ordinary := filepath.Join(root, "ordinary")
	if err := os.Mkdir(ordinary, 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceMetadata(t, filepath.Join(ordinary, ".git"))
	capability, err := OpenGitSourceCapabilityRestricted(t.Context(), ordinary, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := capability.Revalidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	capability.Close()
	private := paths.StateDir()
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	requireRestrictedSource(t, private) // nil EXTRA roots cannot remove private roots
	mutable := filepath.Join(root, "mutable")
	if err := os.Mkdir(mutable, 0700); err != nil {
		t.Fatal(err)
	}
	restriction := localRestriction(t, mutable)
	requireRestrictedSource(t, mutable, restriction)
	requireRestrictedSource(t, root, restriction) // direct child writable below source
	alias := filepath.Join(mutable, "alias")
	if err := os.Symlink(ordinary, alias); err != nil {
		t.Fatal(err)
	}
	requireRestrictedSource(t, alias, restriction) // mutable intermediate cannot be followed
}

func TestSourceProviderRestrictionsReachPointersAlternatesAndMetadataIdentity(t *testing.T) {
	for _, mode := range []string{"pointer", "alternate", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := sourceGuardFixture(t)
			ordinary, mutable := filepath.Join(root, "ordinary"), filepath.Join(root, "mutable")
			for _, name := range []string{ordinary, mutable} {
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var restriction GitSourceRestriction
			switch mode {
			case "pointer":
				writeSourceMetadata(t, mutable)
				if err := os.WriteFile(filepath.Join(ordinary, ".git"), []byte("gitdir: "+mutable+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				restriction = localRestriction(t, mutable)
			case "alternate":
				writeSourceMetadata(t, filepath.Join(ordinary, ".git"))
				writeSourceMetadata(t, mutable)
				if err := os.WriteFile(filepath.Join(ordinary, ".git", "objects", "info", "alternates"), []byte(filepath.Join(mutable, "objects")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				restriction = localRestriction(t, mutable)
			case "hardlink":
				writeSourceMetadata(t, filepath.Join(ordinary, ".git"))
				outside := filepath.Join(mutable, "shared-file")
				if err := os.WriteFile(outside, []byte(".\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, filepath.Join(ordinary, ".git", "commondir")); err != nil {
					t.Fatal(err)
				}
				restriction = localRestriction(t, outside)
			}
			requireRestrictedSource(t, ordinary, restriction)
		})
	}
}

func TestSourceProviderMalformedAndCallerMutatedExtrasDoNotWeakenTheGuard(t *testing.T) {
	root, _ := sourceGuardFixture(t)
	ordinary, sibling := filepath.Join(root, "ordinary"), filepath.Join(root, "sibling")
	for _, name := range []string{ordinary, sibling} {
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeSourceMetadata(t, filepath.Join(ordinary, ".git"))
	for _, restriction := range []GitSourceRestriction{{Path: sibling}, {Path: "relative", Identity: "known"}, {Path: strings.Repeat("x", 5), Identity: "known"}} {
		requireRestrictedSource(t, ordinary, restriction)
	}
	restrictions := []GitSourceRestriction{localRestriction(t, sibling)}
	capability, err := OpenGitSourceCapabilityRestricted(t.Context(), ordinary, true, restrictions...)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	restrictions[0] = localRestriction(t, ordinary)
	if err := capability.Revalidate(t.Context()); err != nil {
		t.Fatalf("caller mutation changed the operation's original restrictions: %v", err)
	}
}
