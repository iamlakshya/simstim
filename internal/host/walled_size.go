package host

import (
	"os"
	"runtime"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func isWalledMemoryV1(m *Manifest) bool {
	if m == nil || m.SandboxOf != "" || m.Wall == nil {
		return false
	}
	if m.Wall.Level == "none" && (m.Wall.Driver == "" || m.Wall.Driver == "none" || m.Wall.Driver == "plain") {
		return false
	}
	// Unknown/inconsistent wall metadata must not acquire none's RSS-only
	// accounting and understate a sandbox's complete native unit cap.
	return true
}

func resolvedWalledSizeV1(values api.SandboxValues, agent agents.Agent) (api.SandboxSize, error) {
	return api.ResolveSandboxSize(values, nil, agent.SandboxSize())
}

func newWalledMemoryV1(a NewArgs, driver string, values api.SandboxValues, agent agents.Agent) (Limits, error) {
	if driver == "plain" {
		asked := max(a.MemoryMax, 0)
		return Limits{MemoryMax: sessionMemoryMax(asked), MemoryAsked: asked}, nil
	}
	asked := max(a.MemoryMax, 0)
	cap := asked
	if driver == "chv" || driver == "firecracker" || driver == "nitro" {
		need, err := vmneed.DecodeV1(a.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return Limits{}, vmNeedError("the VM size has no original complete Need")
		}
		budget, err := vmneed.BudgetFor(need.GuestRAM)
		if err != nil {
			return Limits{}, err
		}
		if asked > 0 && asked != budget.Limit {
			return Limits{}, vmNeedError("the selected VM size understates its complete unit budget")
		}
		cap = budget.Limit
	} else if cap == 0 {
		size, err := resolvedWalledSizeV1(values, agent)
		if err != nil {
			return Limits{}, err
		}
		cap = size.Memory
	}
	if err := checkExactWalledCapV1(cap); err != nil {
		return Limits{}, err
	}
	return Limits{MemoryMax: cap, MemoryAsked: asked, Declared: cap}, nil
}

func checkExactWalledCapV1(cap int64) error {
	if cap < api.MinMemoryMax || cap%int64(os.Getpagesize()) != 0 {
		return UserError("memory_max", "the exact sandbox size cannot be represented by this host's memory cap", "Choose a size of at least 256M that is a whole memory page on this host.")
	}
	return nil
}
