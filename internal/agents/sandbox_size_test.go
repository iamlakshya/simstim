package agents

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestMachineMinimumDoesNotChooseSandboxSize(t *testing.T) {
	agent := Agent{Memory: GB}
	size, err := api.ResolveSandboxSize(api.SandboxValues{}, nil, agent.SandboxSize())
	if err != nil || size.CPUs != 2 || size.Memory != 4<<30 {
		t.Fatalf("machine minimum became a per-sandbox default: %+v %v", size, err)
	}
}

func TestSandboxSizeDataIsCopiedAndLayerStillWins(t *testing.T) {
	agent := Agent{Memory: GB, Size: &api.SandboxSize{CPUs: 3, Memory: 512 << 20}}
	copy := agent.SandboxSize()
	copy.Memory = 256 << 20
	if agent.Size.Memory != 512<<20 {
		t.Fatal("a caller changed the agent table through its size projection")
	}
	memory := "1G"
	size, err := api.ResolveSandboxSize(api.SandboxValues{Memory: &memory}, &api.SandboxSize{CPUs: 4}, agent.SandboxSize())
	if err != nil || size.CPUs != 4 || size.Memory != 1<<30 {
		t.Fatalf("template/layer precedence changed: %+v %v", size, err)
	}
}
