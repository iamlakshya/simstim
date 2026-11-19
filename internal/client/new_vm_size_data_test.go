package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func TestResolveNewVMOptionsUsesAgentSizeRatherThanMachineMinimum(t *testing.T) {
	for _, agent := range agents.Table {
		t.Run(agent.Kind, func(t *testing.T) {
			want, err := api.ResolveSandboxSize(api.SandboxValues{}, nil, agent.SandboxSize())
			if err != nil {
				t.Fatal(err)
			}
			got, err := ResolveNewVMOptions(NewArgs{Agent: agent.Kind}, "microvm")
			if err != nil {
				t.Fatal(err)
			}
			if got.GuestRAM != want.Memory || got.CPUs != want.CPUs {
				t.Fatalf("VM defaults used machine minimum instead of canonical session size: got %d/%d want %d/%d", got.CPUs, got.GuestRAM, want.CPUs, want.Memory)
			}
		})
	}
}
