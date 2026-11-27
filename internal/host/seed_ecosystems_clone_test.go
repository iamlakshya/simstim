package host

import "testing"

func TestProtectedSeedEcosystemsAreOwnedByEveryFilePlanClone(t *testing.T) {
	plan := &FilePlan{Seed: Seed{Repo: "fixture", Ecosystems: []string{"go"}}, Repositories: []Seed{{Repo: "fixture", Ecosystems: []string{"go"}}}}
	owned := CloneFilePlan(plan)
	plan.Seed.Ecosystems[0], plan.Repositories[0].Ecosystems[0] = "python", "rust"
	if owned.Seed.Ecosystems[0] != "go" || owned.Repositories[0].Ecosystems[0] != "go" {
		t.Fatal("protected seed metadata retained source storage")
	}
}
