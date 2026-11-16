package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
 "github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/sandbox"
)

// SandboxStatePlan combines one primary's agent state, configured retained
// files and effective cache directories. Members share that primary's plan.
func SandboxStatePlan(m *Manifest) (files.StatePlan, error) {
	var base files.StatePlan
	if m.Files != nil {
		base = m.Files.State
	}
	var keep []string
	if m.Policy != nil {
		keep = m.Policy.Sandbox.Keep
	}
	kept, err := sandbox.KeptFiles(base, keep)
	if err != nil {
		return files.StatePlan{}, err
	}
	var values *api.SandboxValues
	if m.Policy != nil {
		values = &m.Policy.Sandbox
	}
	plan,err:=sandbox.CacheStatePlan(kept,values)
	if err!=nil{return files.StatePlan{},err}
	paths,err:=proto.DecoyPaths();if err!=nil{return files.StatePlan{},err}
	return files.ExcludeGeneratedHomeState(plan,paths)
}

// SandboxShellCommand applies a configured shell without changing the user's
// command-presence intent, so wake still starts a fresh shell.
func SandboxShellCommand(a api.NewArgs, policy api.Policy, argv []string) ([]string, error) {
	if a.Agent != "shell" || len(a.Argv) > 0 || policy.Sandbox.Shell == nil || *policy.Sandbox.Shell == "" {
		return argv, nil
	}
	if err := sandbox.ValidateShell(*policy.Sandbox.Shell); err != nil {
		return nil, err
	}
	return []string{*policy.Sandbox.Shell}, nil
}
