package host

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// CloneManifest owns mutable DTO/source/opaque data without a JSON roundtrip.
// Policy data delegates to the one canonical API clone.
func CloneManifest(m Manifest) Manifest {
	m.SecretState = api.CloneSessionSecretState(m.SecretState)
	if m.Inputs != nil {
		copy := *m.Inputs
		copy.Repositories = api.CloneInputRepositories(copy.Repositories)
		m.Inputs = &copy
	}
	m.Env = slices.Clone(m.Env)
	m.Secrets = slices.Clone(m.Secrets)
	m.SeedHeads = maps.Clone(m.SeedHeads)
	m.Pulled = slices.Clone(m.Pulled)
	m.MissingSecrets = slices.Clone(m.MissingSecrets)
	m.Labels = maps.Clone(m.Labels)
	m.Run = cloneRuntime(m.Run)
	m.Agent.Argv = slices.Clone(m.Agent.Argv)
	m.Agent.Command = slices.Clone(m.Agent.Command)
	m.Agent.ResumeArgv = slices.Clone(m.Agent.ResumeArgv)
	m.Agent.ContinueArgv = slices.Clone(m.Agent.ContinueArgv)
	m.Agent.Hooks = slices.Clone(m.Agent.Hooks)
	m.Agent.Inline = slices.Clone(m.Agent.Inline)
	m.Agent.IDHistory = slices.Clone(m.Agent.IDHistory)
	m.Layers = api.CloneLayers(m.Layers)
	if m.Policy != nil {
		copy := api.ClonePolicy(*m.Policy)
		m.Policy = &copy
	}
	if m.Wall != nil {
		copy := *m.Wall
		m.Wall = &copy
	}
	if m.Guest != nil {
		copy := *m.Guest
		if copy.ExitCode != nil {
			code := *copy.ExitCode
			copy.ExitCode = &code
		}
		m.Guest = &copy
	}
	if m.Life != nil {
		copy := *m.Life
		if copy.End != nil {
			end := *copy.End
			copy.End = &end
		}
		m.Life = &copy
	}
	if m.Status.ExitCode != nil {
		code := *m.Status.ExitCode
		m.Status.ExitCode = &code
	}
	if m.DebugUntil != nil {
		copy := *m.DebugUntil
		m.DebugUntil = &copy
	}
	if m.Git != nil {
		copy := *m.Git
		copy.skipped = slices.Clone(copy.skipped)
		m.Git = &copy
	}
	m.Files = CloneFilePlan(m.Files)
	if m.RuntimeManifest != nil {
		copy := cloneSelectedRuntime(*m.RuntimeManifest)
		m.RuntimeManifest = &copy
	}
	m.VMNeedV1 = slices.Clone(m.VMNeedV1)
	m.AutomaticRestore = slices.Clone(m.AutomaticRestore)
	m.extra = maps.Clone(m.extra)
	for key, raw := range m.extra {
		m.extra[key] = slices.Clone(raw)
	}
	return m
}
