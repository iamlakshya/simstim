package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeWorkPlanUsesOnlyProtectedSeedAndPullReceiptsV1(t *testing.T) {
	a, b, pulled := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	first, second := Seed{Repo: "first", Commit: a}, Seed{Repo: "second", Commit: b}
	snapshot := &Manifest{ID: "primary2", Files: &FilePlan{Seed: first, Repositories: []Seed{first, second}, PrimaryRepo: "first"}, SeedHeads: map[string]string{"first": a, "second": b}, Pulled: []string{pulled, pulled}}
	args, err := protectedPurgeWorkPlanV1(snapshot)
	if err != nil || len(args.Repositories) != 2 || len(args.Repositories[0].Saved) != 2 || len(args.Repositories[1].Saved) != 2 {
		t.Fatal("protected repository history was lost or duplicated", args, err)
	}
	snapshot.SeedHeads["first"] = b
	if _, err := protectedPurgeWorkPlanV1(snapshot); err == nil {
		t.Fatal("a conflicting protected source became a clean-work plan")
	}
	delete(snapshot.SeedHeads, "first")
	snapshot.Files.Repositories[0].Commit = ""
	snapshot.Files.Seed.Commit = ""
	if _, err := protectedPurgeWorkPlanV1(snapshot); err == nil {
		t.Fatal("missing non-scratch seed authority became an empty clean history")
	}
}

func TestPurgeWorkResultRequiresWholeOriginalRepositorySetV1(t *testing.T) {
	args := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: "primary2", Repositories: []proto.PurgeWorkRepositoryV1{{Name: "first"}, {Name: "second"}}}
	result := proto.PurgeWorkInspectResultV1{Version: 1, Primary: args.Primary, Repositories: []proto.PurgeWorkRepositoryResultV1{{Name: "first"}, {Name: "second", Unpulled: 1}}}
	raw, _ := json.Marshal(result)
	if unsaved, err := decodePurgeWorkResultV1(raw, args); err != nil || !unsaved {
		t.Fatal("complete positive work-loss result was lost", unsaved, err)
	}
	result.Repositories = result.Repositories[:1]
	raw, _ = json.Marshal(result)
	if _, err := decodePurgeWorkResultV1(raw, args); err == nil {
		t.Fatal("a partial guest result implied clean work")
	}
	result.Repositories = []proto.PurgeWorkRepositoryResultV1{{Name: "second"}, {Name: "first"}}
	raw, _ = json.Marshal(result)
	if _, err := decodePurgeWorkResultV1(raw, args); err == nil {
		t.Fatal("a changed repository order implied clean work")
	}
}

func TestPurgeWorkMissingFalseOrZeroCannotImplyCleanV1(t *testing.T) {
	args := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: "primary2", Repositories: []proto.PurgeWorkRepositoryV1{{Name: "repo"}}}
	valid := `{"v":1,"primary":"primary2","outside_repositories":false,"repositories":[{"name":"repo","dirty":false,"unpulled":0}]}`
	if unsaved, err := decodePurgeWorkResultV1(json.RawMessage(valid), args); err != nil || unsaved {
		t.Fatal("explicit complete clean-work result rejected", unsaved, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"outside_repositories":false,`, "", 1),
		strings.Replace(valid, `"outside_repositories":false`, `"outside_repositories":null`, 1),
		strings.Replace(valid, `"dirty":false,`, "", 1),
		strings.Replace(valid, `"dirty":false`, `"dirty":null`, 1),
		strings.Replace(valid, `,"unpulled":0`, "", 1),
		strings.Replace(valid, `"unpulled":0`, `"unpulled":null`, 1),
	} {
		if _, err := decodePurgeWorkResultV1(json.RawMessage(raw), args); err == nil {
			t.Fatal("an absent or null observation implied clean work", raw)
		}
	}
}

func TestPurgeWorkScratchIsOriginalProtectedPlanDataV1(t *testing.T) {
	snapshot := &Manifest{ID: "primary2", Files: &FilePlan{Seed: Seed{Repo: "scratch", Scratch: true}}}
	args, err := protectedPurgeWorkPlanV1(snapshot)
	if err != nil || len(args.Repositories) != 1 || !args.Repositories[0].Scratch || len(args.Repositories[0].Saved) != 0 {
		t.Fatal("actual plain scratch plan was lost", args, err)
	}
	snapshot.Files.Seed.Scratch = false
	if _, err := protectedPurgeWorkPlanV1(snapshot); err == nil {
		t.Fatal("an absent seed in a non-scratch repository became a clean plain workspace")
	}
}
