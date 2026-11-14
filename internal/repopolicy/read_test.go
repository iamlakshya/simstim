package repopolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func policyFile(t *testing.T, root, content string) string {
	t.Helper()
	dir := filepath.Join(root, "."+brand.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sandbox.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRepositoryPolicyReadsExactBytesAndCanonicalProposals(t *testing.T) {
	root := t.TempDir()
	content := "# exact bytes matter\n[sandbox]\npreset='locked'\nwall='process'\n[network]\nallow=['API.EXAMPLE.COM','api.example.com']\ndeny=['*.blocked.example']\nunknown='deny'\n"
	policyFile(t, root, content)
	file, err := Read(root, "shared-repository")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	if file.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash did not cover exact source bytes")
	}
	if len(file.Proposals) != 1 || file.Proposals[0].Target != "api.example.com" || file.Layer.Source != api.SourceRepositoryFile || len(file.Layer.Network.Allow) != 0 {
		t.Fatalf("bad source/proposals: %+v", file)
	}
	if _, err := ReadBytes(t.TempDir()); !os.IsNotExist(err) {
		t.Fatalf("missing file = %v", err)
	}
}

func TestRepositoryPolicyRejectsForbiddenKeysAndAliases(t *testing.T) {
	for _, content := range []string{"[classifier]\n", "[lock]\nnetwork.deny=['api.example.com']\n", "[sandbox]\nprofile='privileged'\n", "[sandbox]\nsecrets=['TOKEN']\n", "[sandbox]\nmounts=['/']\n", "[network]\nhold='1h'\n", "[network]\nallow=['api.example.com']\nALLOW=['other.example.com']\n", "[network]\nrules=[{target='api.example.com',allow=false,remove=true}]\n"} {
		if _, err := Decode("repo", []byte(content)); err == nil {
			t.Fatalf("accepted forbidden file: %s", content)
		}
	}
	file, err := Decode("repo", []byte("[network]\nrules=[{target='api.example.com/path',allow=true,methods=['POST','GET']}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Proposals) != 1 || file.Proposals[0].Methods == nil || strings.Join(*file.Proposals[0].Methods, ",") != "GET,POST" {
		t.Fatal("structured method proposal lost canonical constraints")
	}
}

func TestRepositoryPolicyNeverFollowsEitherSymlinkComponent(t *testing.T) {
	outside := t.TempDir()
	target := policyFile(t, outside, "[network]\nallow=['api.example.com']\n")
	for _, parentLink := range []bool{false, true} {
		root := t.TempDir()
		if parentLink {
			if err := os.Symlink(filepath.Dir(target), filepath.Join(root, "."+brand.Name)); err != nil {
				t.Skip(err)
			}
		} else {
			dir := filepath.Join(root, "."+brand.Name)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, "sandbox.toml")); err != nil {
				t.Skip(err)
			}
		}
		if _, err := Read(root, "repo"); err == nil {
			t.Fatal("repository policy followed a symlink")
		}
	}
}
