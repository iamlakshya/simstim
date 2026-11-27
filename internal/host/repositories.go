package host

import (
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// CloneFilePlan owns every mutable file-plan slice, including legacy and
// canonical repository includes. It does not normalise or change authority.
func CloneFilePlan(plan *FilePlan) *FilePlan {
	if plan == nil {
		return nil
	}
	copy := *plan
	copy.Mounts = slices.Clone(plan.Mounts)
	copy.Installs = slices.Clone(plan.Installs)
	copy.State.Dirs = slices.Clone(plan.State.Dirs)
	copy.State.Files = slices.Clone(plan.State.Files)
	copy.Seed.Includes = slices.Clone(plan.Seed.Includes)
	copy.Seed.Ecosystems = slices.Clone(plan.Seed.Ecosystems)
	copy.Repositories = slices.Clone(plan.Repositories)
	for i := range copy.Repositories {
		copy.Repositories[i].Includes = slices.Clone(plan.Repositories[i].Includes)
		copy.Repositories[i].Ecosystems = slices.Clone(plan.Repositories[i].Ecosystems)
	}
	return &copy
}

// NormaliseRepositories is the only legacy singular FilePlan adapter.
// New plans retain their protected ordered list and explicit primary name.
func (p *FilePlan) NormaliseRepositories() ([]Seed, string, error) {
	if p == nil {
		return nil, "", nil
	}
	repositories := slices.Clone(p.Repositories)
	if len(repositories) == 0 && p.Seed.Repo != "" {
		repositories = []Seed{p.Seed}
	}
	if len(repositories) == 0 {
		return nil, "", UserError("seed_plan", "the protected files plan has no workspace", "Create a fresh sandbox with a source or scratch workspace.")
	}
	primary := p.PrimaryRepo
	if primary == "" {
		primary = repositories[0].Repo
	}
	seen, found := map[string]bool{}, false
	for i := range repositories {
		seed := &repositories[i]
		seed.Includes = slices.Clone(seed.Includes)
		seed.Ecosystems = slices.Clone(seed.Ecosystems)
		if !api.InputRepoName(seed.Repo) || seen[seed.Repo] || !seed.Scratch && !api.ValidGitOID(seed.Commit) {
			return nil, "", UserError("seed_plan", "the protected repository list is invalid or has duplicate names", "Use distinct repository basenames before starting the sandbox.")
		}
		seen[seed.Repo] = true
		found = found || seed.Repo == primary
	}
	if !found {
		return nil, "", UserError("seed_plan", "the protected primary repository is missing", "Create the sandbox again with a valid primary source.")
	}
	if len(p.Repositories) != 0 && p.Seed.Repo != "" {
		index := slices.IndexFunc(repositories, func(seed Seed) bool { return seed.Repo == primary })
		if !reflect.DeepEqual(p.Seed, repositories[index]) {
			return nil, "", UserError("seed_plan", "the legacy workspace and repository list disagree", "Repair the protected files plan before starting it.")
		}
	}
	return repositories, primary, nil
}

// SelectRepository chooses one protected clone without inspecting guest Git.
// Multiple clones require an explicit --repo name. Returned paths are guest
// paths and must never become a host Git working directory.
func (p *FilePlan) SelectRepository(name string) (string, error) {
	repositories, _, err := p.NormaliseRepositories()
	if err != nil {
		return "", err
	}
	var names []string
	for _, repository := range repositories {
		if repository.Scratch {
			continue
		}
		names = append(names, repository.Repo)
	}
	if name == "" && len(names) == 1 {
		name = names[0]
	}
	if !slices.Contains(names, name) {
		return "", UserError("pull_repo", "choose a repository from: "+strings.Join(names, ", "), "Pass --repo NAME when pulling a sandbox with several repositories.")
	}
	return path.Join("/work", name), nil
}
