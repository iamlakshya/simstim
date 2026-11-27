package image

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (s *Store) prepareAgentsPublication(ctx context.Context, initial *agentsSourceV1, disk AgentDiskV1, temporary string, output *os.File, reused bool, handedOff *bool) (*Lease, error) {
	name := "agents/" + disk.Content.SHA256 + ".lock"
	for {
		// Immutable completed bytes need no writer lease. In particular an
		// already running VM may retain this SAME shared disk indefinitely.
		shared, err := s.lock(ctx, name, true)
		if err != nil {
			return nil, err
		}
		stable, err := s.checkedAgentsCandidate(ctx, disk)
		if err != nil {
			return nil, errors.Join(err, shared.Close())
		}
		if stable {
			if err := s.commitAgentsSourceLocked(ctx, initial, disk, temporary, output, reused, handedOff); err != nil {
				return nil, errors.Join(err, shared.Close())
			}
			return shared, nil
		}
		if err := shared.Close(); err != nil {
			return nil, err
		}
		// Two first publishers can race after their read. A blocking writer
		// here could wait on the winner's long-lived returned shared lease.
		// Try once through the SAME native owner/identity checks, then read
		// again under shared ownership if a different publisher won.
		exclusive, acquired, err := s.tryExistingLease(ctx, name, false)
		if err != nil {
			return nil, err
		}
		if !acquired {
			if err := waitImageLeaseRetry(ctx); err != nil {
				return nil, err
			}
			continue
		}
		_, err = s.checkedAgentsCandidate(ctx, disk)
		if err == nil {
			err = s.commitAgentsSourceLocked(ctx, initial, disk, temporary, output, reused, handedOff)
		}
		if err != nil {
			return nil, errors.Join(err, exclusive.Close())
		}
		// Downgrade the owned descriptor before returning or opening a gap
		// in retention. waitLease repeats the exact post-lock validation.
		return s.shareLease(ctx, name, exclusive)
	}
}

// checkedAgentsCandidate is called while shared or exclusive disk ownership
// is already held. Pending parts need a later exclusive replay; completed
// content can be used by simultaneous starts without waiting for a VM exit.
func (s *Store) checkedAgentsCandidate(ctx context.Context, disk AgentDiskV1) (bool, error) {
	root, err := s.agentsRoot(ctx, false)
	if err != nil {
		return false, err
	}
	defer root.Close()
	record, _, err := s.readAgentsRecord(root, disk.Content.SHA256)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if record.Purge != nil {
		return false, agentsDiskError("the selected agents disk is being removed; its original receipt was preserved")
	}
	if err := checkAgentsRecordBytes(root, record, disk.Content); err != nil {
		return false, err
	}
	return record.Leaf == disk.Content.SHA256+".img", ctx.Err()
}

func checkAgentsRecordWitness(root *os.Root, record agentsRecordV1) error {
	leaf, err := agentsRecordLeaf(root, record)
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o444)
	if err != nil {
		return err
	}
	return errors.Join(record.File.CheckFile(root, leaf, file, 0o444), file.Close())
}
