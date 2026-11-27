package client

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func policySeedDiscovery(t *testing.T, dir string) *inputSeedDiscovery {
	t.Helper()
	d, err := discoverSessionSeeds(t.Context(), dir, []string{dir}, false, OpenLocalGitMetadataSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func TestSeedPolicyReadsSelectedCommitAndRefusesChangedHEAD(t *testing.T) {
	dir := laptopSeedFixture(t)
	name := filepath.Join("."+brand.Name, "sandbox.toml")
	committed := "[sandbox]\nwall='process'\n"
	pullFixtureCommit(t, dir, name, committed)
	d := policySeedDiscovery(t, dir)
	copyFixture(t, dir, name, "this is invalid TOML from the working tree\n")
	policy, err := d.primaryPolicy(t.Context(), false, "", OpenLocalGitMetadataSource)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(committed))
	if policy == nil || policy.Hash != hex.EncodeToString(want[:]) {
		t.Fatalf("policy not selected commit: %+v", policy)
	}
	if err := d.revalidatePrimaryPolicy(t.Context()); err != nil {
		t.Fatal(err)
	}
	pullFixtureCommit(t, dir, "another.txt", "changed HEAD\n")
	if err := d.revalidatePrimaryPolicy(t.Context()); err == nil {
		t.Fatal("advanced HEAD reused original policy authority")
	}
}

func TestDirtySeedPolicyReusesOneProspectiveCommit(t *testing.T) {
	dir := laptopSeedFixture(t)
	name := filepath.Join("."+brand.Name, "sandbox.toml")
	pullFixtureCommit(t, dir, name, "[sandbox]\nwall='process'\n")
	d := policySeedDiscovery(t, dir)
	selected := "[sandbox]\nwall='microvm'\n"
	copyFixture(t, dir, name, selected)
	policy, err := d.primaryPolicy(t.Context(), true, "", OpenLocalGitMetadataSource)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := d.seeds[0].policy
	if policy == nil || snapshot.commit == snapshot.head {
		t.Fatal("dirty policy did not bind one extra commit")
	}
	copyFixture(t, dir, name, "[sandbox]\nwall='process'\n")
	seed, err := createLaptopSeedSelected(t.Context(), dir, true, api.DefaultSeedMax, snapshot.identity, "", snapshot, OpenLocalGitMetadataSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := seed.close(); err != nil {
			t.Error(err)
		}
	})
	if seed.declaration.Commit != snapshot.commit {
		t.Fatal("bundle recaptured a different dirty commit")
	}
	if got := pullFixtureGit(t, dir, "cat-file", "blob", snapshot.commit+":"+filepath.ToSlash(name)); got != gitLine([]byte(selected)) {
		t.Fatalf("prospective policy = %q", got)
	}
	if _, err := os.Stat(snapshot.scratch); err != nil {
		t.Fatalf("original private index was discarded before plan closure: %v", err)
	}
}
