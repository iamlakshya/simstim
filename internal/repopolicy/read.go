// Package repopolicy reads untrusted repository policy through the shared
// configuration schema and canonical policy target/merge primitives.
package repopolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/configdata"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Read reads one bounded regular descriptor from the original
// repository. OpenRegular refuses symlinks in every component and special
// files without blocking. No repository command or script is executed here.
func Read(rootPath, repo string) (*api.RepositoryPolicy, error) {
	data, err := ReadBytes(rootPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Decode(repo, data)
}

// ReadBytes pins the same bounded no-follow descriptor for config checking.
func ReadBytes(rootPath string) ([]byte, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, repoPolicyError("cannot open the source repository", err)
	}
	defer root.Close()
	directory := "." + brand.Name
	parent, err := root.Lstat(directory)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, repoPolicyError("cannot inspect the repository policy folder", err)
	}
	if parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() {
		return nil, repoPolicyError("repository policy folder is not a real directory", nil)
	}
	relative := directory + "/sandbox.toml"
	if entry, err := root.Lstat(relative); os.IsNotExist(err) {
		return nil, os.ErrNotExist
	} else if err != nil {
		return nil, repoPolicyError("cannot inspect the repository policy path", err)
	} else if entry.Mode()&os.ModeSymlink != 0 {
		return nil, repoPolicyError("repository policy is a symlink", nil)
	}
	file, err := files.OpenRegular(root, relative)
	if os.IsNotExist(err) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, repoPolicyError("cannot read the repository policy as a regular file", err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, repoPolicyError("cannot inspect the repository policy", err)
	}
	if stat.Size() > configdata.MaxFileSize {
		return nil, repoPolicyError("repository policy exceeds the configuration size limit", nil)
	}
	data, err := io.ReadAll(io.LimitReader(file, configdata.MaxFileSize+1))
	if err != nil {
		return nil, repoPolicyError("cannot read the repository policy", err)
	}
	if len(data) > configdata.MaxFileSize {
		return nil, repoPolicyError("repository policy exceeds the configuration size limit", nil)
	}
	return data, nil
}

// Decode applies the shared typed schema to one exact file version.
func Decode(repo string, data []byte) (*api.RepositoryPolicy, error) {
	var values api.LayerValues
	_, table, err := configdata.DecodeTable(string(data), &values)
	if err != nil {
		return nil, repoPolicyError("repository policy is not valid TOML", err)
	}
	if err := ValidateTable(table); err != nil {
		return nil, err
	}
	if err := egress.ValidateLayerValues(values); err != nil {
		return nil, repoPolicyError("repository policy has an invalid value", err)
	}
	hash := sha256.Sum256(data)
	out := &api.RepositoryPolicy{Repo: repo, Hash: hex.EncodeToString(hash[:]), Layer: api.Layer{Kind: api.LayerRepo, Name: repo, Source: api.SourceRepositoryFile, LayerValues: values}}
	out.Layer.Network.Allow = nil
	proposals := map[string]api.Rule{}
	emptyQuestion, _ := json.Marshal(api.GrantQuestion{Repo: repo, Hash: out.Hash, Proposals: []api.Rule{}})
	proposalBytes := len(emptyQuestion)
	addProposal := func(rule api.Rule) error {
		if previous, known := proposals[rule.ID]; known {
			if err := egress.ValidateRuleIdentities([]api.Rule{previous, rule}); err != nil {
				return err
			}
			return nil
		}
		encoded, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		proposalBytes += len(encoded)
		if len(proposals) > 0 {
			proposalBytes++
		}
		if proposalBytes+1 > proto.MaxFrame {
			return repoPolicyError("repository proposals exceed the protocol response bound", nil)
		}
		proposals[rule.ID] = rule
		return nil
	}
	for _, target := range values.Network.Allow {
		canonical, err := egress.ValidateRuleTarget(target)
		if err != nil {
			return nil, err
		}
		rule := api.Rule{Layer: api.LayerRepo, Name: repo, Source: api.SourceRepositoryFile, Allow: true, Door: api.DoorNet, Target: canonical}
		rule.ID = api.RuleID(rule)
		if err := addProposal(rule); err != nil {
			return nil, err
		}
	}
	var retained []api.NetworkRule
	for _, raw := range values.Network.Rules {
		canonical, err := egress.CanonicalNetworkRule(raw)
		if err != nil {
			return nil, err
		}
		if canonical.Remove {
			return nil, forbiddenRepositoryKey("network.rules.remove")
		}
		if !canonical.Allow {
			retained = append(retained, canonical)
			continue
		}
		rule := api.Rule{Layer: api.LayerRepo, Name: repo, Source: api.SourceRepositoryFile, Allow: true, Door: api.DoorNet, Target: canonical.Target, Methods: canonical.Methods}
		rule.ID = api.RuleID(rule)
		if err := addProposal(rule); err != nil {
			return nil, err
		}
	}
	out.Layer.Network.Rules = retained
	current, cleaned, err := egress.MergeLayers([]api.Layer{out.Layer})
	if err != nil {
		return nil, err
	}
	identities := append([]api.Rule(nil), current.Rules...)
	for _, rule := range proposals {
		identities = append(identities, rule)
	}
	if err := egress.ValidateRuleIdentities(identities); err != nil {
		return nil, err
	}
	if len(cleaned) != 1 {
		return nil, repoPolicyError("repository policy layer is inconsistent", nil)
	}
	out.Layer = cleaned[0]
	for _, rule := range proposals {
		out.Proposals = append(out.Proposals, rule)
	}
	out.Layer.Network.Allow = nil
	sort.Slice(out.Proposals, func(i, j int) bool { return out.Proposals[i].ID < out.Proposals[j].ID })
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, repoPolicyError("cannot encode repository metadata", err)
	}
	if len(encoded)+1 > proto.MaxFrame {
		return nil, repoPolicyError("repository metadata exceeds the protocol response bound", nil)
	}
	return out, nil
}

// ValidateTable restricts the actual shared schema, preserving its exact
// alias/type rules. The repository cannot choose a judge, profile or resource.
func ValidateTable(table map[string]any) error {
	for _, key := range configdata.InspectConfigTable(table, reflect.TypeFor[api.LayerValues]()).Keys {
		if key.Type == nil || key.Name != "sandbox" && key.Name != "network" {
			return forbiddenRepositoryKey(key.Spelling)
		}
		child, ok := table[key.Spelling].(map[string]any)
		if !ok {
			return forbiddenRepositoryKey(key.Spelling)
		}
		typ := reflect.TypeFor[api.SandboxValues]()
		if key.Name == "network" {
			typ = reflect.TypeFor[api.NetworkValues]()
		}
		for _, field := range configdata.InspectConfigTable(child, typ).Keys {
			allowed := AllowedKey([]string{key.Name, field.Name})
			if field.Type == nil || !allowed {
				return forbiddenRepositoryKey(key.Spelling + "." + field.Spelling)
			}
		}
	}
	return nil
}

func forbiddenRepositoryKey(key string) error {
	return &proto.Error{Code: "layer_key", Message: "the repository file cannot set " + key, Hint: "Keep only sandbox.wall, sandbox.preset and network allow, deny, rules or unknown in the repository file."}
}
func repoPolicyError(message string, err error) error {
	if err != nil {
		message += ": " + err.Error()
	}
	return &proto.Error{Code: "repo_policy", Message: message, Hint: fmt.Sprintf("Review .%s/sandbox.toml in the original repository, then start again.", brand.Name)}
}

// AllowedKey is the sole repository-file subset of the actual shared schema.
// A repository can narrow traffic; it cannot choose a judge or grant resources.
func AllowedKey(path []string) bool {
	if len(path) == 0 {
		return true
	}
	if len(path) == 1 {
		return path[0] == "sandbox" || path[0] == "network"
	}
	if path[0] == "sandbox" {
		return len(path) == 2 && (path[1] == "wall" || path[1] == "preset")
	}
	if path[0] != "network" {
		return false
	}
	if len(path) == 2 {
		return path[1] == "allow" || path[1] == "deny" || path[1] == "unknown" || path[1] == "rules"
	}
	return len(path) == 3 && path[1] == "rules" && (path[2] == "target" || path[2] == "allow" || path[2] == "methods" || path[2] == "remove")
}
