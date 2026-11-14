package host

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func inputSummaryFixture(t *testing.T, name string) *api.InputSummary {
	t.Helper()
	nonce, err := api.NewInputNonce()
	if err != nil {
		t.Fatal(err)
	}
	plan := api.SessionInputs{Version: api.SessionInputsV1, Nonce: nonce, Identity: api.GitIdentity{Known: true}, Repositories: []api.InputRepository{{Name: name, SourceBranch: "refs/heads/laptop-seed", Commit: strings.Repeat("a", 40), ObjectFormat: "sha1", Size: 1, SHA256: strings.Repeat("b", 64)}}}
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	return &summary
}

func TestInputRepositoriesUseLiteralNamesAndExactProtectedSeedHeads(t *testing.T) {
	m := &Manifest{ID: "abcd2345", Files: &FilePlan{Seed: Seed{Repo: "host: repo", Commit: strings.Repeat("c", 40)}}}
	summary := inputSummaryFixture(t, "laptop: repo")
	if err := addLaptopRepositories(m, summary, true); err != nil {
		t.Fatal(err)
	}
	if m.Cwd != "/work/host: repo" || m.Files.Repositories[1].Repo != "laptop: repo" || m.SeedHeads["host: repo"] != strings.Repeat("c", 40) || m.SeedHeads["laptop: repo"] != strings.Repeat("a", 40) {
		t.Fatal("selected literal repositories or exact seed heads were lost")
	}
	summary.Repositories[0].Commit = strings.Repeat("d", 40)
	if m.Inputs.Repositories[0].Commit != strings.Repeat("a", 40) {
		t.Fatal("protected input summary aliases the caller")
	}
	legacy := &FilePlan{Seed: Seed{Repo: "old-slug", Commit: strings.Repeat("e", 40)}}
	repositories, _, err := legacy.NormaliseRepositories()
	if err != nil || repositories[0].Repo != "old-slug" {
		t.Fatal("normaliser re-derived a protected legacy name", err)
	}
}

func TestInputRepositoryCollisionAndImplicitScratchRules(t *testing.T) {
	m := &Manifest{ID: "abcd2345", Files: &FilePlan{Seed: Seed{Repo: "same: repo", Commit: strings.Repeat("c", 40)}}}
	if err := addLaptopRepositories(m, inputSummaryFixture(t, "same: repo"), true); err == nil {
		t.Fatal("equal literal basenames were accepted")
	}
	m = &Manifest{ID: "abcd2345"}
	if err := addLaptopRepositories(m, inputSummaryFixture(t, "laptop: repo"), false); err != nil {
		t.Fatal(err)
	}
	if len(m.Files.Repositories) != 1 || m.Files.Seed.Scratch || m.Files.Seed.Repo != "laptop: repo" || m.Cwd != "/work/laptop: repo" {
		t.Fatal("implicit host scratch displaced the first laptop repository")
	}
}
