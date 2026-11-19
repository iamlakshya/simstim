package image

import (
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/credentialpaths"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SaveHome is the fixed guest ABI home. It never comes from sandbox.env.
const SaveHome = "/home/agent"

// SavedRootInputMax includes the raw root plus the bounded changes prelude.
// The guest layer itself remains bounded by MaxUncompressed.
const SavedRootInputMax = MaxUncompressed + proto.ImageChangesBytes + 4*proto.ImageChangesMax + 4

// SavedRootPlan is ephemeral converter input derived by the original runtime
// owner. It grants no root descriptor or publication authority. OriginalState
// remains unfiltered so invalid declarations cannot disappear behind an opt-out.
type SavedRootPlan struct {
	Version       int                   `json:"v"`
	SaveID        string                `json:"save_id,omitempty"`
	Ref           string                `json:"ref,omitempty"`
	OriginalState files.StatePlan       `json:"state"`
	ActualState   files.StatePlan       `json:"actual_state"`
	ImagePath     []string              `json:"image_path"`
	Values        map[string]api.Secret `json:"values"`
	Changes       []string              `json:"-"`
}

// Format keeps every private scanner value out of diagnostics.
func (SavedRootPlan) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }

// NewSavedRootPlan captures owned data from the existing original source.
// Callers must retain that source and its real root-stream lifetime separately.
func NewSavedRootPlan(original, actual files.StatePlan, imagePath string, values map[string]api.Secret, changes []string) (SavedRootPlan, error) {
	plan := SavedRootPlan{Version: 1, OriginalState: files.StatePlan{Dirs: slices.Clone(original.Dirs), Files: slices.Clone(original.Files)}, ActualState: files.StatePlan{Dirs: slices.Clone(actual.Dirs), Files: slices.Clone(actual.Files)}, Changes: slices.Clone(changes), Values: make(map[string]api.Secret, len(values))}
	for name, value := range values {
		plan.Values[name] = value
	}
	if imagePath != "" {
		plan.ImagePath = strings.Split(imagePath, ":")
	}
	_, err := newSavedRootFilter(plan)
	return plan, err
}

type savedRootFilter struct {
	exclusions []SaveFinding
	paths      []string
}

func newSavedRootFilter(plan SavedRootPlan) (*savedRootFilter, error) {
	if plan.Version != 1 {
		return nil, saveFailure("image_bad_layer", "the saved root plan has an unsupported version")
	}
	// Validate the original plan BEFORE removing generated files, even when
	// package settings or decoys are disabled for this run.
	generated, err := proto.DecoyPaths()
	if err != nil {
		return nil, err
	}
	packages, err := proto.PackageManagerPaths()
	if err != nil {
		return nil, err
	}
	generated = append(generated, packages...)
	if _, err := files.ExcludeGeneratedHomeState(plan.OriginalState, generated); err != nil {
		return nil, saveFailure("image_bad_layer", "the original saved-root state plan is invalid")
	}
	if _, err := files.ExcludeGeneratedHomeState(plan.ActualState, generated); err != nil {
		return nil, saveFailure("image_bad_layer", "the actual original saved-root state plan is invalid")
	}
	if err := validateSavedChanges(plan.Changes); err != nil {
		return nil, err
	}
	filter := &savedRootFilter{}
	add := func(name, reason string) {
		filter.exclusions = append(filter.exclusions, SaveFinding{Path: name, Reason: reason})
	}
	for _, name := range []string{"/proc", "/sys", "/dev", "/work", "/mnt", "/tmp", "/var/tmp", "/run", "/opt/agents", "/opt/" + brand.Name, "/.tools", "/.persist", "/.oldroot"} {
		add(name, "runtime or session filesystem")
	}
	state, err := plan.ActualState.ProtectedPaths(SaveHome)
	if err != nil {
		return nil, err
	}
	for _, name := range state {
		add(name, "agent state or kept cache")
	}
	for _, agent := range agents.Table {
		for _, name := range agent.StateDirs {
			if !files.Relative(name) {
				return nil, saveFailure("image_bad_layer", "the canonical agent state path is invalid")
			}
			add(path.Join(SaveHome, name), "agent state")
		}
		if agent.Setup == nil {
			continue
		}
		for _, name := range agent.Setup.LoginFiles {
			if !strings.HasPrefix(name, "~/") || !files.Relative(strings.TrimPrefix(name, "~/")) {
				return nil, saveFailure("image_bad_layer", "the canonical agent login path is invalid")
			}
			add(path.Join(SaveHome, strings.TrimPrefix(name, "~/")), "agent login file")
		}
	}
	for _, name := range generated {
		add(path.Join(SaveHome, name), "generated session setting or decoy")
	}
	for _, home := range []string{SaveHome, "/root"} {
		for _, folder := range credentialpaths.Folders() {
			add(path.Join(home, folder), "private home folder")
		}
	}
	for _, folder := range append(slices.Clone(plan.ImagePath), path.Join(SaveHome, ".local/bin"), "/root/.local/bin") {
		if !savedAbsolutePath(folder) {
			return nil, saveFailure("image_bad_layer", "the original image PATH is not an absolute clean path list")
		}
		filter.paths = append(filter.paths, folder)
	}
	slices.Sort(filter.paths)
	filter.paths = slices.Compact(filter.paths)
	sortSaveFindings(filter.exclusions)
	return filter, nil
}

func validateSavedChanges(changes []string) error {
	if len(changes) > proto.ImageChangesMax {
		return tooLarge("the saved image has too many changed paths")
	}
	total := 0
	for _, name := range changes {
		total += len(name)
		if !savedAbsolutePath(name) || total > proto.ImageChangesBytes {
			return saveFailure("image_bad_layer", "the image changes list is invalid or exceeds its bound")
		}
	}
	return nil
}

func savedBelow(name, parent string) bool {
	return name == parent || parent == "/" || strings.HasPrefix(name, parent+"/")
}

func (filter *savedRootFilter) exclude(name string) string {
	for _, exclusion := range filter.exclusions {
		if savedBelow(name, exclusion.Path) {
			return exclusion.Reason
		}
	}
	for _, home := range []string{SaveHome, "/root"} {
		if !savedBelow(name, home) || name == home {
			continue
		}
		for _, component := range strings.Split(strings.TrimPrefix(name, home+"/"), "/") {
			if reason := credentialpaths.PrivateName(component); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func sortSaveFindings(findings []SaveFinding) {
	slices.SortFunc(findings, func(a, b SaveFinding) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.Reason, b.Reason)
	})
}
