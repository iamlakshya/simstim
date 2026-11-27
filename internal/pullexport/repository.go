package pullexport

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// GuestRepository is a protected plan supplied by the existing primary owner.
// Its guest path never becomes a host Git path or a source of native authority.
type GuestRepository struct {
	Name  string
	Cwd   string
	Seeds []string
}

// SelectRepository restricts the SAME retained source once, before Capture.
func (s *GuestSource) SelectRepository(name string) error {
	s.selectMu.Lock()
	defer s.selectMu.Unlock()
	if name != "" && !api.InputRepoName(name) {
		return exportError("pull_repo", "the selected repository basename is invalid")
	}
	if s.selected {
		if s.repository != name {
			return exportError("pull_repo", "a retained pull cannot change its repository")
		}
		return nil
	}
	plans := s.options.Repositories
	if len(plans) == 0 {
		if name != "" {
			return exportError("pull_repo", "the legacy source has no protected repository list")
		}
		s.selected, s.repository = true, name
		return nil
	}
	selected := -1
	if name == "" && len(plans) == 1 {
		selected = 0
	}
	for index, plan := range plans {
		if plan.Name == name {
			selected = index
		}
	}
	if selected < 0 {
		return exportError("pull_repo", "choose one protected repository basename with --repo")
	}
	s.options.Cwd, s.options.Seeds = plans[selected].Cwd, slices.Clone(plans[selected].Seeds)
	s.selected, s.repository = true, name
	return nil
}
