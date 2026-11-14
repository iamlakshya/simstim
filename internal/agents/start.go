package agents

import (
	"fmt"
	"slices"
)

// StartSelection is the canonical command/kind resolution shared by fresh
// local planning and the host. Known means the caller named a table agent.
type StartSelection struct {
	Kind  string
	Agent Agent
	Known bool
	Argv  []string
}

// ResolveStart preserves normal new's shell/custom and explicit-command
// semantics. It selects no secret names, credentials, grants or policy.
func ResolveStart(kind string, argv []string) (StartSelection, error) {
	definition, known := Find(kind)
	selected := StartSelection{Kind: kind, Agent: definition, Known: known}
	switch {
	case len(argv) > 0:
		selected.Argv = slices.Clone(argv)
		if selected.Kind == "" {
			selected.Kind = "custom"
		}
	case known:
		selected.Argv = slices.Clone(definition.Launch)
	case kind == "":
		selected.Agent, _ = Find("shell")
		selected.Argv, selected.Kind = slices.Clone(selected.Agent.Launch), "shell"
	default:
		return StartSelection{}, fmt.Errorf("unknown agent %q", kind)
	}
	return selected, nil
}
