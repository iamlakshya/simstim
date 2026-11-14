package agents

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// StateKind says whether a kept home path is a folder or a copied file.
type StateKind string

const (
	// StateDirectory stays in place as a writable private folder.
	StateDirectory StateKind = "directory"
	// StateFile is copied with its SQLite side files at a clean stop.
	StateFile StateKind = "file"
)

// StatePath is the internal typed plan for one entry of Agent.StateDirs.
// The public StateDirs list keeps its specified []string wire format.
type StatePath struct {
	Path string
	Kind StateKind
}

type agentFiles struct {
	state   []StatePath
	install []string
	project []string
}

// fileData is the only definition of state paths and their types. Missing
// first-start files cannot be typed by stat, their suffix, or an agent name
// in another package. See isolation spec 7.1 and 8.6 for these paths.
var fileData = map[string]agentFiles{
	"claude": {
		state:   []StatePath{{".claude/projects", StateDirectory}},
		install: []string{".local/share/claude"},
		project: []string{".claude/", "CLAUDE.md", ".mcp.json"},
	},
	"codex": {
		state: []StatePath{
			{".codex/sessions", StateDirectory},
			{".codex/archived_sessions", StateDirectory},
			{".codex/state_5.sqlite", StateFile},
		},
		project: []string{"AGENTS.md", ".codex/"},
	},
}

func withFileData(table []Agent) []Agent {
	for i := range table {
		data := fileData[table[i].Kind]
		table[i].StateDirs = stateNames(data.state)
		table[i].InstallDirs = slices.Clone(data.install)
		table[i].ProjectFiles = slices.Clone(data.project)
	}
	return table
}

func stateNames(plan []StatePath) []string {
	var names []string
	for _, entry := range plan {
		names = append(names, entry.Path)
	}
	return names
}

// StatePlan returns the types that the canonical data gives StateDirs.
// An unknown or changed entry fails instead of guessing a missing file's
// type or making a directory where the agent expects a database.
func (a Agent) StatePlan() ([]StatePath, error) {
	plan := fileData[a.Kind].state
	if !slices.Equal(a.StateDirs, stateNames(plan)) {
		return nil, fmt.Errorf("agent state paths have no matching type data")
	}
	for _, entry := range plan {
		if !fs.ValidPath(entry.Path) || entry.Path == "." || strings.ContainsRune(entry.Path, '\\') {
			return nil, fmt.Errorf("agent state path must stay beneath the home")
		}
		if entry.Kind != StateDirectory && entry.Kind != StateFile {
			return nil, fmt.Errorf("agent state path has no known type")
		}
	}
	return slices.Clone(plan), nil
}

// AllStatePlan lists the state of every agent, so another agent can join a
// sandbox later. It rejects overlapping paths and conflicting file types.
func AllStatePlan() ([]StatePath, error) {
	var out []StatePath
	for _, agent := range Table {
		plan, err := agent.StatePlan()
		if err != nil {
			return nil, err
		}
		for _, entry := range plan {
			duplicate := false
			for _, old := range out {
				if old == entry {
					duplicate = true
					break
				}
				if old.Path == entry.Path || strings.HasPrefix(old.Path, entry.Path+"/") || strings.HasPrefix(entry.Path, old.Path+"/") {
					return nil, fmt.Errorf("agent state paths overlap")
				}
			}
			if !duplicate {
				out = append(out, entry)
			}
		}
	}
	return out, nil
}
