package host

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/wall"
)

// FilePlan is the trusted, non-secret plan for one sandbox's files.
type FilePlan struct {
	// Repositories is the protected ordered clone plan. Seed mirrors its primary.
	Repositories []Seed          `json:"repositories,omitempty"`
	PrimaryRepo  string          `json:"primary_repo,omitempty"`
	Seed         Seed            `json:"seed"`
	Mounts       []wall.Mount    `json:"mounts,omitempty"`
	Installs     []wall.Mount    `json:"installs,omitempty"`
	State        files.StatePlan `json:"state"`
}

// Seed records the committed source and the explicit include exposures.
// The bundle and include bytes live in the trusted session folder.
type Seed struct {
	// Laptop marks a bundle from the authenticated client input phase.
	Laptop       bool       `json:"laptop,omitempty"`
	SourceBranch string     `json:"source_branch,omitempty"`
	Repo         string     `json:"repo"`
	Commit       string     `json:"commit,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	Origin       string     `json:"origin,omitempty"`
	BundleSize   int64      `json:"bundle_size,omitempty"`
	Includes     []SeedFile `json:"includes,omitempty"`
	Scratch      bool       `json:"scratch,omitempty"`
}

// SeedFile is an explicitly included regular file, relative to the repo.
type SeedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func allAgentState() (files.StatePlan, error) {
	typed, err := agents.AllStatePlan()
	if err != nil {
		return files.StatePlan{}, err
	}
	var plan files.StatePlan
	for _, entry := range typed {
		switch entry.Kind {
		case agents.StateDirectory:
			plan.Dirs = append(plan.Dirs, entry.Path)
		case agents.StateFile:
			plan.Files = append(plan.Files, entry.Path)
		}
	}
	return plan, plan.Validate()
}

func workspaceMode(m *Manifest) string {
	if m.Files == nil {
		return ""
	}
	if direct := files.ExplainMount(m.Files.Mounts); direct != "" {
		if m.Files.Seed.Scratch {
			return "scratch, direct folder"
		}
		return direct
	}
	if m.Files.Seed.Scratch {
		return "scratch"
	}
	return ""
}
