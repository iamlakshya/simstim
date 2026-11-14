package host

import (
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

// ApplyRepositoryResume re-reads the original working tree before any stop.
// Existing file grants disappear when their file changes or is removed. The
// client's old hash never approves the new content, and the wall is not lowered.
func ApplyRepositoryResume(m *Manifest, a api.ResumeArgs) error {
	info, err := repositorySourceInfo(m.Cwd)
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) && pe.Code == "not_a_repo" {
			info = &api.RepoInfo{}
		} else {
			return err
		}
	}
	if info == nil {
		return UserError("repo_policy", "source repository metadata is missing", "Update the helper before resuming.")
	}
	var base []api.Layer
	for _, layer := range m.Layers {
		if layer.Source != api.SourceRepositoryFile {
			base = append(base, layer)
		}
	}
	file := info.Policy
	if file != nil {
		if a.GrantHash != file.Hash {
			return questionError(api.CodeGrantQuestion, "the repository policy changed before this start", "Review this exact file in the client before resuming.", api.GrantQuestion{Repo: file.Repo, Hash: file.Hash, Proposals: file.Proposals})
		}
		layer, _, err := egress.PrepareRepositoryLayer(file, egress.RepositoryBroader(base), a.GrantHash, a.GrantAnswers)
		if err != nil {
			return err
		}
		if m.Wall == nil {
			if len(layer.Rules) > 0 || !reflect.ValueOf(layer.LayerValues).IsZero() {
				return UserError("not_walled", "the repository now needs sandbox policy", "Start a new walled session to use this repository policy.")
			}
			return nil
		}
		if level := layer.Sandbox.Wall; level != nil {
			asked, err := wall.ParseLevel(*level)
			if err != nil {
				return err
			}
			running, err := wall.ParseLevel(m.Wall.Level)
			if err != nil {
				return err
			}
			if asked > running {
				return UserError("repo_policy_wall", "the repository now requires a stronger wall", "Start a new session with the required wall; this resume keeps its creation wall.")
			}
		}
		base = append(base, layer)
	}
	if m.Wall == nil {
		return nil
	}
	policy, clean, err := egress.MergeLayers(base)
	if err != nil {
		return err
	}
	m.Policy, m.Layers = &policy, clean
	return nil
}
