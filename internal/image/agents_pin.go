package image

import (
	"context"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/supply"
)

// PinAgentsV1 retains an exact earlier canonical-producer publication. It
// never snapshots current installations or substitutes the newest disk.
func (s *Store) PinAgentsV1(ctx context.Context, expected supply.ContentRef, arch string) (disk AgentDiskV1, retained *Lease, returned error) {
	file, retained, err := s.OpenAgentsV1(ctx, expected)
	if err != nil {
		return disk, nil, err
	}
	defer func() {
		returned = errors.Join(returned, file.Close())
		if returned != nil {
			returned = errors.Join(returned, retained.Close())
			retained = nil
		}
	}()
	root, err := s.agentsRoot(ctx, false)
	if err != nil {
		return disk, retained, err
	}
	defer root.Close()
	record, _, err := s.readAgentsRecord(root, expected.SHA256)
	if err != nil {
		return disk, retained, err
	}
	disk = record.Disk
	if disk.Arch != arch {
		// Empty or architecture-independent bytes can have one SHA on two
		// architectures. Only a canonical CURRENT producer pointer can supply
		// that current inventory metadata; the ContentRef stays byte-exact.
		source, err := s.existingLease(ctx, "agents.lock", true)
		if err != nil {
			return disk, retained, err
		}
		current, readErr := s.readAgentsSource(root)
		closeErr := source.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return disk, retained, err
		}
		if current.Disk.Content != expected {
			return disk, retained, agentsDiskError("the selected agents disk has another producer architecture")
		}
		disk = current.Disk
	}
	disk.Agents = slices.Clone(disk.Agents)
	return disk, retained, disk.Validate(arch)
}
