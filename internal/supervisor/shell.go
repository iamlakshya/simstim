package supervisor

import (
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func shellStartArgs(m *host.Manifest, a *proto.CtlStartArgs) {
	if m.Agent.Kind == "shell" && len(m.Agent.Command) == 0 {
		a.DefaultShell = true
		a.Shell = host.SandboxShell(m)
	}
}
