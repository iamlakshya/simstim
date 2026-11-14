package client

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// CapProbe asks the helper for a fresh sandbox capability probe. An empty
// releaseURL makes no release reachability request.
func (c *Conn) CapProbe(ctx context.Context, releaseURL string) (api.CapProbe, error) {
	if !c.HasFeature("sandbox") {
		return api.CapProbe{}, &UserError{
			Msg:  fmt.Sprintf("the helper on %s is too old for sandboxes", CleanLine(c.Host.Name)),
			Hint: brand.Name + " host upgrade " + ShellQuote(c.Host.Name),
			Code: "wall_helper_old",
			Kind: KindFailed,
		}
	}
	var out api.CapProbe
	err := c.Call(ctx, "sandbox.probe", struct {
		ReleaseURL string `json:"release_url,omitempty"`
	}{releaseURL}, &out)
	return out, err
}

// CleanWallProbes returns a copy of each driver result for the terminal.
// The unmodified results remain available for policy checks and JSON.
func CleanWallProbes(probes []api.WallProbe) []api.WallProbe {
	if probes == nil {
		return nil
	}
	out := make([]api.WallProbe, len(probes))
	for i, p := range probes {
		p.Driver, p.Level = CleanLine(p.Driver), CleanLine(p.Level)
		p.Reason, p.Fix, p.Version = CleanLine(p.Reason), CleanLine(p.Fix), CleanLine(p.Version)
		p.Facts = cleanMap(p.Facts, CleanLine)
		out[i] = p
	}
	return out
}

// CleanCapProbe returns a copy of the sandbox probe with host text cleaned
// for the terminal. It does not change the result used by policy checks.
func CleanCapProbe(p api.CapProbe) api.CapProbe {
	p.OS, p.Arch, p.Kernel = CleanLine(p.OS), CleanLine(p.Arch), CleanLine(p.Kernel)
	p.Distro, p.User, p.CPUVirt = CleanLine(p.Distro), CleanLine(p.User), CleanLine(p.CPUVirt)
	p.Virt, p.DiskFS = CleanLine(p.Virt), CleanLine(p.DiskFS)
	p.KVM.Mode, p.KVM.Group = CleanLine(p.KVM.Mode), CleanLine(p.KVM.Group)
	p.Delegated = cleanLines(p.Delegated, CleanLine)
	p.RootSteps = cleanLines(p.RootSteps, CleanLine)
	p.Runtimes = cleanLines(p.Runtimes, CleanLine)
	p.Walls = CleanWallProbes(p.Walls)
	if p.Nitro != nil {
		n := *p.Nitro
		if n.Pools != nil {
			n.Pools = make(map[string]int64, len(p.Nitro.Pools))
			for name, count := range p.Nitro.Pools {
				n.Pools[CleanLine(name)] = count
			}
		}
		p.Nitro = &n
	}
	if p.ReleaseReachable != nil {
		reachable := *p.ReleaseReachable
		p.ReleaseReachable = &reachable
	}
	return p
}
