package client

import (
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// SessionRuntimeHealth is display-only. A session's self-reported versions
// never select expected runtime bytes or grant compatibility readiness.
type SessionRuntimeHealth struct {
	Outdated bool
	Exposed  bool
	Hint     string
}

func RuntimeHealth(session Session) SessionRuntimeHealth {
	release, err := ReleaseManifest()
	if err != nil || release == nil {
		return SessionRuntimeHealth{}
	}
	return sessionRuntimeHealthV1(session, *release)
}

func sessionRuntimeHealthV1(session Session, release supply.Manifest) SessionRuntimeHealth {
	release.Advisories = supply.MergeAdvisories(release.Advisories, supply.KnownBad())
	terminal := SessionRuntimeHealth{Exposed: session.HostVersion != "" && CleanLine(session.HostVersion) == session.HostVersion && len(release.Matches(supply.AdvisoryTerminal, brand.VTBinary, session.HostVersion)) > 0}
	if terminal.Exposed {
		terminal.Hint = "End the session when it is idle and start a new one with the current client."
	}
	result := terminal
	observed := session.Runtime
	if observed == nil || observed.Version != 1 || len(observed.Items) == 0 || len(observed.Items) > 64 {
		return result
	}
	seen := map[string]bool{}
	for _, item := range observed.Items {
		key := item.Kind + "\x00" + item.Name + "\x00" + item.Arch
		if !runtimecache.IsRuntime(item.Kind) || item.Name == "" || CleanLine(item.Name) != item.Name || item.Arch != "amd64" && item.Arch != "arm64" || item.Version == "" || CleanLine(item.Version) != item.Version || !supply.IsHex64(item.SHA256) || seen[key] {
			return terminal
		}
		seen[key] = true
		kind := supply.AdvisoryKind(item.Kind)
		if kind != "" && len(release.Matches(kind, item.Name, item.Version)) > 0 {
			result.Exposed = true
		}
		for _, current := range release.Items {
			if current.Kind == item.Kind && current.Name == item.Name && current.Arch == item.Arch && current.SHA256 != item.SHA256 && (current.Version == item.Version || supply.Below(item.Version, current.Version)) {
				result.Outdated = true
			}
		}
	}
	if result.Outdated || result.Exposed {
		result.Hint = "End the session when it is idle and start a new one with the current client."
	}
	return result
}
