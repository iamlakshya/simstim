package egress

import (
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

// PrepareRepositoryLayer applies only narrowing defaults and denies, then
// exact-hash answers. It uses the canonical merge/target helpers, not another
// matcher. Loosening defaults are ignored with a note, as spec8.5 requires.
func PrepareRepositoryLayer(file *api.RepositoryPolicy, broader []api.Layer, hash string, answers map[string]bool) (api.Layer, []string, error) {
	if file == nil {
		return api.Layer{}, nil, nil
	}
	if !api.ValidPolicyHash(file.Hash) || file.Repo == "" || file.Layer.Kind != api.LayerRepo || file.Layer.Source != api.SourceRepositoryFile || file.Layer.Name != file.Repo || file.Layer.Lock != nil {
		return api.Layer{}, nil, badRepositoryData()
	}
	layer := api.CloneLayers([]api.Layer{file.Layer})[0]
	layer.ReviewedHash = ""
	if len(layer.Network.Allow) > 0 {
		return api.Layer{}, nil, badRepositoryData()
	}
	for _, rule := range layer.Rules {
		if rule.Allow {
			return api.Layer{}, nil, badRepositoryData()
		}
	}
	if err := ValidateLayerValues(layer.LayerValues); err != nil {
		return api.Layer{}, nil, err
	}
	before, _, err := MergeLayers(broader)
	if err != nil {
		return api.Layer{}, nil, err
	}
	var notes []string
	if preset := layer.Sandbox.Preset; preset != nil {
		if presetRank(api.Preset(*preset)) >= presetRank(before.Preset) {
			if presetRank(api.Preset(*preset)) > presetRank(before.Preset) {
				notes = append(notes, "The repository's looser preset "+*preset+" is ignored.")
			}
			layer.Sandbox.Preset = nil
			layer.Preset = ""
		}
	}
	if unknown := layer.Network.Unknown; unknown != nil {
		if unknownRank(api.Unknown(*unknown)) <= unknownRank(before.Unknown) {
			if unknownRank(api.Unknown(*unknown)) < unknownRank(before.Unknown) {
				notes = append(notes, "The repository's looser unknown action "+*unknown+" is ignored.")
			}
			layer.Network.Unknown = nil
			layer.Unknown = ""
		}
	}
	if level := layer.Sandbox.Wall; level != nil {
		old := "none"
		if before.Sandbox.Wall != nil {
			old = *before.Sandbox.Wall
		}
		if *level == "auto" || levelRank(*level) <= levelRank(old) {
			if *level == "auto" || levelRank(*level) < levelRank(old) {
				notes = append(notes, "The repository's weaker wall "+*level+" is ignored.")
			}
			layer.Sandbox.Wall = nil
		}
	}
	if _, _, err := MergeLayers(append(slices.Clone(broader), layer)); err != nil {
		return api.Layer{}, nil, err
	}
	proposals, err := canonicalRepositoryProposals(file)
	if err != nil {
		return api.Layer{}, nil, err
	}
	// Check identity against every broader/file deny BEFORE a question or
	// answer is trusted, without applying unapproved allowances to locks.
	current, _, err := MergeLayers(append(slices.Clone(broader), layer))
	if err != nil {
		return api.Layer{}, nil, err
	}
	identities := append(slices.Clone(current.Rules), proposals...)
	if err := ValidateRuleIdentities(identities); err != nil {
		return api.Layer{}, nil, err
	}
	if len(proposals) > 0 {
		complete := hash == file.Hash
		for _, rule := range proposals {
			if _, ok := answers[rule.ID]; !ok {
				complete = false
			}
		}
		if !complete {
			question := api.GrantQuestion{Repo: file.Repo, Hash: file.Hash, Proposals: proposals, Notes: notes}
			data, _ := json.Marshal(question)
			return layer, notes, &proto.Error{Code: api.CodeGrantQuestion, Message: "the repository proposes network allowances", Hint: "Review the proposals in the client. Each answer is bound to this file's hash.", Data: data}
		}
		for _, rule := range proposals {
			if answers[rule.ID] {
				layer.Rules = append(layer.Rules, rule)
			}
		}
		layer.ReviewedHash = file.Hash
	}
	// True answers take effect only after the same final canonical merge.
	// False answers remain possible even when a lock forbids the proposal.
	_, cleaned, err := MergeLayers(append(slices.Clone(broader), layer))
	if err != nil {
		return api.Layer{}, nil, err
	}
	for _, candidate := range cleaned {
		if candidate.Kind == api.LayerRepo && candidate.Name == file.Repo && candidate.Source == api.SourceRepositoryFile {
			layer = candidate
			break
		}
	}

	return layer, notes, nil
}

func badRepositoryData() error {
	return &proto.Error{Code: "repo_policy", Message: "repository policy metadata is malformed or incompatible", Hint: "Update the helper and review the repository policy file."}
}
func presetRank(p api.Preset) int {
	switch p {
	case api.PresetLocked:
		return 0
	case api.PresetBalanced:
		return 1
	case api.PresetOpen:
		return 2
	default:
		return -1
	}
}
func unknownRank(u api.Unknown) int {
	switch u {
	case api.UnknownAllow:
		return 0
	case api.UnknownClassify:
		return 1
	case api.UnknownAsk:
		return 2
	case api.UnknownDeny:
		return 3
	default:
		return -1
	}
}
func levelRank(level string) int {
	if level == "auto" {
		return 0
	}
	parsed, err := wall.ParseLevel(level)
	if err != nil {
		return -1
	}
	return int(parsed)
}

// RepositoryBroader returns only the trusted layers before the file layer.
func RepositoryBroader(layers []api.Layer) []api.Layer {
	var out []api.Layer
	for _, layer := range layers {
		if layer.Source == "" && api.LayerRank(layer.Kind) <= api.LayerRank(api.LayerRepo) {
			out = append(out, layer)
		}
	}
	return out
}

func canonicalRepositoryProposals(file *api.RepositoryPolicy) ([]api.Rule, error) {
	proposals := make([]api.Rule, 0, len(file.Proposals))
	for _, input := range file.Proposals {
		rule := input
		rule.Layer, rule.Name, rule.Source = api.LayerRepo, file.Repo, api.SourceRepositoryFile
		if !rule.Allow || rule.Door != api.DoorNet || rule.Once || !rule.Until.IsZero() || rule.Mode != "" || rule.GrantID != "" {
			return nil, badRepositoryData()
		}
		canonical, err := canonicalIdentityRule(rule)
		if err != nil {
			return nil, err
		}
		canonical.ID = api.RuleID(canonical)
		if canonical.ID != input.ID {
			return nil, badRepositoryData()
		}
		proposals = append(proposals, canonical)
	}
	return CanonicalRuleInstances(proposals)
}
