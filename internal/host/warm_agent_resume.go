package host

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// PrepareWarmAgentResumeV1 applies the canonical command and environment rules
// to one owned snapshot. This pure metadata preparation grants no native right.
// Its caller must retain and check the original live controller separately.
func PrepareWarmAgentResumeV1(saved, parent *Manifest, environment *api.SessionEnvironment) (*Manifest, string, error) {
	if saved == nil || parent == nil || !api.ValidSessionID(saved.ID) || saved.Wall == nil || parent.Wall == nil || saved.ID != parent.ID && saved.SandboxOf != parent.ID {
		return nil, "", memberError("the original warm member changed")
	}
	if err := ValidateManifestStart(saved); err != nil {
		return nil, "", err
	}
	m := CloneManifest(*saved)
	argv, why, err := resumeArgv(&m)
	if err != nil {
		return nil, "", err
	}
	if m.Agent.Kind == "shell" && len(m.Agent.Command) == 0 {
		argv = slices.Clone(m.Agent.Argv)
		if len(argv) == 0 {
			return nil, "", memberError("the saved shell command is empty")
		}
		if IsNoneSupervisor(parent) {
			argv[0] = LoginShell()
		} else {
			argv[0] = SandboxShell(parent)
		}
	}
	m.Agent.Argv = argv
	if IsNoneSupervisor(&m) {
		m.RunHook = resumeHook(&m)
	}
	m.Cwd, m.HostLabel, m.SandboxName = parent.Cwd, parent.HostLabel, parent.SandboxName
	if err := prepareResumeEnvironment(&m, environment); err != nil {
		return nil, "", err
	}
	applySessionIdentity(&m)
	return &m, why, nil
}
