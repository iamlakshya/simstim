package supervisor

import (
	"sort"

	"github.com/iamlakshya/simstim/internal/guest"
)

func (s *Supervisor) buildStartReq(secretEnv, notes []string) guest.StartReq {
	var pkgLevel, minAge string
	var userEnv []string
	if s.m.Wall != nil {
		pkgLevel = s.m.Wall.PackageManagers
		minAge = s.m.Wall.MinReleaseAge
		if len(s.m.Wall.SandboxEnv) > 0 {
			keys := make([]string, 0, len(s.m.Wall.SandboxEnv))
			for k := range s.m.Wall.SandboxEnv {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			userEnv = make([]string, 0, len(keys))
			for _, k := range keys {
				userEnv = append(userEnv, k+"="+s.m.Wall.SandboxEnv[k])
			}
		}
	}
	return guest.StartReq{
		Session:         s.id,
		Name:            s.m.Name,
		Agent:           s.m.Agent.Kind,
		Argv:            s.m.Agent.Argv,
		Env:             s.m.Env,
		Secrets:         secretEnv,
		Cwd:             s.m.Cwd,
		Term:            s.m.Term.Term,
		Cols:            s.m.Term.Cols,
		Rows:            s.m.Term.Rows,
		RunHook:         s.m.RunHook,
		Notes:           notes,
		AgentID:         s.m.Agent.AgentSessionID,
		IDSource:        s.m.Agent.IDSource,
		PackageManagers: pkgLevel,
		MinReleaseAge:   minAge,
		UserEnv:         userEnv,
	}
}
