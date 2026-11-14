package image

import (
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

const (
	// MaxAgentsDisk is the complete ext4 output limit, including metadata.
	MaxAgentsDisk int64 = 4 << 30
	// MaxAgentsEntries includes real and synthetic entries in the disk.
	MaxAgentsEntries = 500000
)

// AgentDiskV1 is inventory from the canonical host-installed-agent producer.
// It is outside G11. Validation and a ContentRef alone create no provenance;
// an authenticated client selects this receipt before H5 binds the exact Need.
type AgentDiskV1 struct {
	Version      int               `json:"version"`
	Arch         string            `json:"arch"`
	SourceDigest string            `json:"source_digest"`
	Content      supply.ContentRef `json:"content"`
	Agents       []string          `json:"agents"`
}

// Validate checks the fixed role, architecture and stated disk caps. It does
// not authorise refresh, select runtime pins or trust inventory as a program.
func (d AgentDiskV1) Validate(arch string) error {
	if d.Version != 1 || d.Arch != arch || arch != "amd64" && arch != "arm64" || !supply.IsHex64(d.SourceDigest) {
		return agentsDiskError("the agents disk inventory has an invalid version, architecture or source digest")
	}
	if err := ValidateAgentsContentV1(d.Content); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, kind := range d.Agents {
		if kind == "" || seen[kind] {
			return agentsDiskError("the agents disk inventory repeats or omits an agent identity")
		}
		seen[kind] = true
	}
	return nil
}

func agentsDiskError(detail string) error {
	return &proto.Error{Code: "agents_disk", Message: "could not build the agents disk: " + detail, Hint: "Check the host's installed agents and free space with host doctor, then try again."}
}

// ValidateAgentsContentV1 checks the one fixed installed-agent content role
// and disk cap. It creates no producer, client or execution provenance.
func ValidateAgentsContentV1(ref supply.ContentRef) error {
	if err := ref.Validate(); err != nil {
		return agentsDiskError("the expected agents content reference is invalid")
	}
	if ref.Name != "agents" || ref.Size > MaxAgentsDisk || ref.Size%4096 != 0 {
		return agentsDiskError("the expected agents disk has another role, size or alignment")
	}
	return nil
}
