package api

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/supply"
)

// FeatureSandboxPrepareV1 names the fixed test boot and joined cleanup contract.
const FeatureSandboxPrepareV1 = "sandbox-prepare-v1"

// Fixed test-boot dimensions belong to the actual prepare producer. They are
// not caller-selectable settings or a substitute for native admission.
const (
	SandboxPrepareGuestRAMV1       int64 = 512 << 20
	SandboxPrepareCPUsV1                 = 1
	SandboxPrepareDiskV1           int64 = 1 << 30
	SandboxPrepareTimeoutSecondsV1       = 60
)

// SandboxPrepareArgsV1 carries only an original client's selected artifacts.
// The native bridge issuer still requires its same-connection producer owners.
// No field can select a command, environment, value, grant, seed or guest size.
type SandboxPrepareArgsV1 struct {
	Version  int              `json:"version"`
	Level    string           `json:"level"`
	Runtime  *supply.Manifest `json:"runtime,omitempty"`
	VMNeedV1 json.RawMessage  `json:"vm_need_v1,omitempty"`
}

// SandboxPrepareResultV1 is display data after a real fixed boot and cleanup.
// Copying it cannot publish a floor, cache entry, runtime or native boot grant.
type SandboxPrepareResultV1 struct {
	Version int    `json:"version"`
	Level   string `json:"level"`
	Driver  string `json:"driver"`
	BootMS  int64  `json:"boot_ms"`
}
