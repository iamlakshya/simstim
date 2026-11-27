package guest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This opt-in witness uses the real UID1000 command runner and system Git.
// It proves the reader body only; it creates no host native boot/stop grant.
func TestNativePurgeWorkReadsActualSavedDirtyAndUnpulledStateV1(t *testing.T) {
	for _, changed := range []bool{false, true} {
		g, _, bundle := nativeBundleGuest(t, changed, true)
		args := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: bundle.Primary, Repositories: []proto.PurgeWorkRepositoryV1{{Name: "repo", Saved: bundle.SeedHeads}}}
		raw, _ := json.Marshal(args)
		result, err := g.inspectPurgeWorkV1(raw)
		if err != nil || len(result.Repositories) != 1 || result.Repositories[0].Dirty || (result.Repositories[0].Unpulled != 0) != changed {
			t.Fatal("fixed native reader changed actual committed work", result, err)
		}
		if err := os.WriteFile(filepath.Join(g.root, "work", "repo", "untracked.txt"), []byte("save this"), 0600); err != nil {
			t.Fatal(err)
		}
		result, err = g.inspectPurgeWorkV1(raw)
		if err != nil || !result.Repositories[0].Dirty {
			t.Fatal("actual untracked work was reported clean", result, err)
		}
		if err := os.WriteFile(filepath.Join(g.root, "work", "outside.txt"), []byte("copy this"), 0600); err != nil {
			t.Fatal(err)
		}
		result, err = g.inspectPurgeWorkV1(raw)
		if err != nil || !result.OutsideRepositories {
			t.Fatal("actual files outside the protected repositories were lost", result, err)
		}
	}
}

func TestNativePurgeWorkReadsActualPlainScratchDirectoryV1(t *testing.T) {
	g, _, bundle := nativeBundleGuest(t, false, false)
	scratch := filepath.Join(g.root, "work", "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	args := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: bundle.Primary, Repositories: []proto.PurgeWorkRepositoryV1{{Name: "repo", Saved: bundle.SeedHeads}, {Name: "scratch", Scratch: true}}}
	raw, _ := json.Marshal(args)
	result, err := g.inspectPurgeWorkV1(raw)
	if err != nil || len(result.Repositories) != 2 || result.Repositories[1].Dirty || result.Repositories[1].Unpulled != 0 || result.OutsideRepositories {
		t.Fatal("actual empty plain scratch was not known clean", result, err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "work.txt"), []byte("copy this"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = g.inspectPurgeWorkV1(raw)
	if err != nil || !result.Repositories[1].Dirty {
		t.Fatal("actual plain scratch work was lost", result, err)
	}
	args.Repositories[1].Scratch = false
	raw, _ = json.Marshal(args)
	if _, err := g.inspectPurgeWorkV1(raw); err == nil {
		t.Fatal("a missing Git directory without the protected scratch plan implied clean work")
	}
}
