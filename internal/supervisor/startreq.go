package supervisor

import (
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func (s *Supervisor) buildStartReq(secretEnv, notes []string) guest.StartReq {
	var pkgLevel, minAge, proxyURL, caCertPEM, boundSecretEnv, subscriptionRoute string
	var userEnv, boundPlaceholders, cacheDirs, keep []string
	if s.egress != nil {
		caCertPEM = s.egress.CACertPEM()
	}
	if s.m.Wall != nil {
		pkgLevel = s.m.Wall.PackageManagers
		minAge = s.m.Wall.MinReleaseAge
		cacheDirs = slices.Clone(s.m.Wall.CacheDirs)
		keep = slices.Clone(s.m.Wall.Keep)
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
		if (s.m.Wall.Driver == process.DriverName || s.m.Wall.Driver == "chv" || s.m.Wall.Driver == "gvisor") && !s.m.Wall.ShareNet {
			proxyURL = process.GuestProxyURL
			boundSecretEnv = s.agentBoundSecretEnv()
			subscriptionRoute = s.subscriptionRoute()
			boundPlaceholders = s.boundPlaceholders(boundSecretEnv)
		}
	}
	return guest.StartReq{
		Session:           s.id,
		Name:              s.m.Name,
		Agent:             s.m.Agent.Kind,
		Argv:              s.m.Agent.Argv,
		Env:               s.m.Env,
		Secrets:           secretEnv,
		Cwd:               s.m.Cwd,
		Term:              s.m.Term.Term,
		Cols:              s.m.Term.Cols,
		Rows:              s.m.Term.Rows,
		RunHook:           s.m.RunHook,
		Notes:             notes,
		AgentID:           s.m.Agent.AgentSessionID,
		IDSource:          s.m.Agent.IDSource,
		PackageManagers:   pkgLevel,
		MinReleaseAge:     minAge,
		UserEnv:           userEnv,
		CacheDirs:         cacheDirs,
		ProxyURL:          proxyURL,
		CACertPEM:         caCertPEM,
		BoundSecretEnv:    boundSecretEnv,
		SubscriptionRoute: subscriptionRoute,
		BoundPlaceholders: boundPlaceholders,
		Keep:              keep,
	}
}
