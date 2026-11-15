package host

import (
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// PrepareRepositoryNewArgs re-reads the original source before reservation,
// unit start or any repository command. Carried file layers are never trusted.
func PrepareRepositoryNewArgs(a NewArgs) (NewArgs, error) {
	for _, layer := range a.Layers {
		if layer.Source != "" || layer.ReviewedHash != "" {
			return a, UserError("bad_request", "new may carry only laptop policy sources", "Let the host read the repository policy file.")
		}
	}
	if a.Inputs!=nil {
		for index,repository:=range a.Inputs.Repositories {
			if repository.Policy!=nil && (a.Cwd!="" || index!=0) {return a,UserError("bad_request","only the original laptop primary may declare repository policy","Prepare the selected primary policy and inputs again.")}
		}
		if len(a.Inputs.Repositories)>0 && a.Cwd=="" {return prepareLaptopPrimaryPolicy(a)}
	}
	info, err := repositorySourceInfo(a.Cwd)
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) && pe.Code == "not_a_repo" {
			return a, nil
		}
		return a, err
	}
	if info == nil {
		return a, UserError("repo_policy", "source repository metadata is missing", "Update the host helper and start again.")
	}
	file := info.Policy
	if file == nil {
		return a, nil
	}
	if a.GrantHash != file.Hash {
		return a, questionError(api.CodeGrantQuestion, "the repository policy changed or has not been checked", "Refresh and review this exact file before starting.", api.GrantQuestion{Repo: file.Repo, Hash: file.Hash, Proposals: file.Proposals})
	}
	layer, _, err := egress.PrepareRepositoryLayer(file, egress.RepositoryBroader(a.Layers), a.GrantHash, a.GrantAnswers)
	if err != nil {
		return a, err
	}
	if (a.Wall == "" || a.Wall == "none") && len(layer.Rules) == 0 && reflect.ValueOf(layer.LayerValues).IsZero() {
		return a, nil
	}
	a.Layers = append(api.CloneLayers(a.Layers), layer)
	if _, _, err := egress.MergeLayers(a.Layers); err != nil {
		return a, err
	}
	return a, nil
}

// repositorySourceInfo is the one trusted git-op read used by lifecycle checks.
// Tests replace only this transport; regular/no-follow reads are tested intact.
var repositorySourceInfo = func(dir string) (*api.RepoInfo, error) {
	answer, err := gitOp(gitOpRepoInfo, gitOpRequest{RepoInfoArgs: api.RepoInfoArgs{Dir: expandHome(dir), Policy: true}})
	return answer.Repo, err
}
