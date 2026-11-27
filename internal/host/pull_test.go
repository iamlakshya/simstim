package host

import (
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPullMaxDistinguishesAbsentFromExplicitInvalid(t *testing.T) {
	if got, err := EffectivePullMax(&Manifest{}); err != nil || got != api.PullMaxDefault {
		t.Fatalf("absent: %d %v", got, err)
	}
	for _, value := range []string{"", "bogus", "512", "0M"} {
		m := &Manifest{Policy: &api.Policy{}}
		m.Policy.Sandbox.PullMax = &value
		if _, err := EffectivePullMax(m); err == nil {
			t.Fatal("explicit invalid cap silently refilled", value)
		}
	}
	value := "1GiB"
	m := &Manifest{Policy: &api.Policy{}}
	m.Policy.Sandbox.PullMax = &value
	if got, err := EffectivePullMax(m); err != nil || got != 1<<30 {
		t.Fatalf("applied canonical size: %d %v", got, err)
	}
}

func TestProtectedSeedHistoryNeverUsesGuestClaims(t *testing.T) {
	oid := strings.Repeat("a", 40)
	m := &Manifest{Files: &FilePlan{Seed: Seed{Commit: oid}}}
	heads, err := ProtectedSeedHeads(m)
	if err != nil || len(heads) != 1 || heads[0] != oid {
		t.Fatal(heads, err)
	}
	m.SeedHeads = map[string]string{"repo": "invalid guest text"}
	if _, err := ProtectedSeedHeads(m); err == nil {
		t.Fatal("invalid protected history became an empty default")
	}
}

func TestUnwalledPullCannotRunGitForSandboxRecord(t *testing.T) {
	m := &Manifest{ID: "fixture2", Cwd: t.TempDir()}
	plain, err := NewUnwalledPullSource(m, func() error { return nil })
	if err != nil {
		t.Fatal("ordinary-source positive control failed", err)
	}
	if err = plain.Close(); err != nil {
		t.Fatal(err)
	}
	m.Wall = &WallRecord{Driver: "process"}
	if _, err := NewUnwalledPullSource(m, func() error { return nil }); err == nil {
		t.Fatal("sandbox files entered the host Git operation")
	}
}

func TestRepositorySourcePreservesTrailingSpaceDirectory(t *testing.T) {
	repo := setupRepo(t)
	spaced := repo + " "
	if err := os.Rename(repo, spaced); err != nil {
		t.Fatal(err)
	}
	info, err := repoInfo(spaced, false)
	if err != nil || info.Root != spaced || info.Main != spaced {
		t.Fatal("repository metadata changed the actual source pathname", info, err)
	}
}

func TestStoppedSandboxRowKeepsProtectedWallAndStoppedState(t *testing.T) {
	m := &Manifest{ID: "fixture2", Status: Status{State: StateStopped}, Wall: &WallRecord{Driver: "process", Level: "process"}}
	row := infoFrom(m, false)
	if row.State != api.StateStopped || row.Wall == nil || row.Wall.Driver != "process" {
		t.Fatal("stopped pull lost the actual session routing facts", row)
	}
}
