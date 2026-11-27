package client

import (
	"context"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func (c *Conn) prepareRepositoryPolicy(ctx context.Context, a NewArgs) (NewArgs, error) {
	info, err := SessionRepoPolicy(ctx, c.Host, a.Cwd)
	if err != nil {
		var ue *UserError
		if errors.As(err, &ue) && ue.Code == "not_a_repo" {
			a.RepositoryPolicy = nil
			a.GrantHash = ""
			a.GrantAnswers = nil
			return a, nil
		}
		return a, err
	}
	return c.prepareRepositoryPolicyFile(ctx, a, info.Policy)
}

func (c *Conn) prepareRepositoryPolicyFile(ctx context.Context, a NewArgs, file *api.RepositoryPolicy) (NewArgs, error) {
	if file == nil {
		a.RepositoryPolicy = nil
		a.GrantHash = ""
		a.GrantAnswers = nil
		return a, nil
	}
	if !c.HasFeature(api.RepositoryPolicyFeature) {
		return a, c.grantHelperOld("check repository policy before fetching secrets")
	}
	if a.GrantHash != file.Hash {
		answers, found, err := LoadGrantAnswers(ctx, c.Host.Name, file.Repo, file.Hash)
		if err != nil {
			return a, err
		}
		if found {
			a.GrantHash, a.GrantAnswers = file.Hash, answers
		}
	}
	layer, notes, err := egress.PrepareRepositoryLayer(file, egress.RepositoryBroader(a.Layers), a.GrantHash, a.GrantAnswers)
	copy := *file
	copy.Proposals = api.CloneLayers([]api.Layer{{Rules: file.Proposals}})[0].Rules
	copy.Layer = layer
	copy.Notes = append(slices.Clone(file.Notes), notes...)
	a.RepositoryPolicy = &copy
	if err != nil {
		return a, policyError(err)
	}
	a.GrantHash = file.Hash
	return a, nil
}

// StartPolicyLayers includes the checked repository-file preview without
// putting host-sourced data into NewArgs.Layers or this laptop's source pin.
func StartPolicyLayers(a NewArgs) []api.Layer {
	layers := api.CloneLayers(a.Layers)
	if a.RepositoryPolicy != nil {
		layers = append(layers, api.CloneLayers([]api.Layer{a.RepositoryPolicy.Layer})[0])
	}
	return layers
}

func laptopStartLayers(layers []api.Layer) []api.Layer {
	var out []api.Layer
	for _, layer := range layers {
		if layer.Source != api.SourceRepositoryFile {
			out = append(out, layer)
		}
	}
	return out
}

// GrantQuestionOf decodes only a complete, hash-bound repository question.
func GrantQuestionOf(err error) (api.GrantQuestion, bool) {
	return questionOf(err, api.CodeGrantQuestion, validGrantQuestion)
}
