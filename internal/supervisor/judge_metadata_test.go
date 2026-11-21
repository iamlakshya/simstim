package supervisor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestJudgeMetadataUsesOriginalProtectedSeedAndTaskAndOwnsTags(t *testing.T) {
	manifest := &host.Manifest{Task: "user task", Agent: host.AgentInfo{Kind: "shell"}, Files: &host.FilePlan{Seed: host.Seed{Origin: "https://name:password@example.com/team/repo.git?token=excluded#fragment"}}}
	profile := "review"
	policy := api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Profile: &profile}}}
	tags := []string{"go", "python"}
	manifest.Files.Seed.Ecosystems = tags
	metadata := trustedJudgeMetadata(manifest, policy)
	tags[0] = "mutated"
	manifest.Task, manifest.Files.Seed.Origin = "later task", "https://later.example/repo"
	if metadata.Task != "user task" || metadata.Agent != "shell" || metadata.Profile != "review" || metadata.RepositoryRemote != "https://example.com/team/repo.git" || !reflect.DeepEqual(metadata.Ecosystems, []string{"go", "python"}) {
		t.Fatal("judge metadata lost its original protected copy")
	}
	if strings.Contains(metadata.RepositoryRemote, "password") || strings.Contains(metadata.RepositoryRemote, "token=") {
		t.Fatal("source credentials entered judge metadata")
	}
}

func TestJudgeScratchAndUnknownMetadataFabricateNoRepositoryFacts(t *testing.T) {
	manifest := &host.Manifest{Files: &host.FilePlan{Seed: host.Seed{Scratch: true, Origin: "https://example.com/repo", Ecosystems: []string{"go"}}}}
	metadata := trustedJudgeMetadata(manifest, api.Policy{})
	if metadata.RepositoryRemote != "" || len(metadata.Ecosystems) != 0 {
		t.Fatal("scratch workspace acquired repository facts")
	}
	manifest.Files.Seed.Scratch = false
	manifest.Files.Seed.Ecosystems = []string{"unknown"}
	metadata = trustedJudgeMetadata(manifest, api.Policy{})
	if len(metadata.Ecosystems) != 0 {
		t.Fatal("unknown ecosystem tag entered judge input")
	}
}
