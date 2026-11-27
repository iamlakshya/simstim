package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

// PurgeAgentsOwned removes receipt-owned agents disks after the host has
// drained producers and stopped owned sessions. References are reread under
// exclusive native disk ownership. This is distinct from newest retention;
// unknown, foreign and referenced objects remain explicitly reported.
func (s *Store) PurgeAgentsOwned(ctx context.Context, refs References) (PurgeReport, error) {
	var report PurgeReport
	if refs == nil {
		return report, agentsDiskError("trusted agents references are unavailable for purge")
	}
	if err := s.domain.Validate(); err != nil {
		return report, err
	}
	root, directory, err := s.agentsPurgeRoot(ctx)
	if errors.Is(err, os.ErrNotExist) {
		if _, inspectErr := s.root.Lstat("agents"); inspectErr == nil {
			report.Left = append(report.Left, PurgeLeft{"agents", "the agents child has no canonical producer directory receipt"})
			return report, nil
		}
		return s.finishAbsentAgents(ctx, refs, report)
	}
	if err != nil {
		return report, err
	}
	if root != nil {
		defer root.Close()
		candidates, err := s.agentsCandidates(root)
		if err != nil {
			return report, err
		}
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			lease, acquired, err := s.tryExistingLease(ctx, "agents/"+candidate.Digest+".lock", false)
			if err != nil || !acquired {
				reason := "the agents disk still has a live native lease"
				if err != nil {
					reason = err.Error()
				}
				report.Left = append(report.Left, PurgeLeft{"agents/" + candidate.Digest, reason})
				continue
			}
			did, collectErr := s.collectAgentsLocked(ctx, root, candidate, refs, false)
			collectErr = errors.Join(collectErr, lease.Close())
			if collectErr != nil {
				report.Left = append(report.Left, PurgeLeft{"agents/" + candidate.Digest, collectErr.Error()})
			} else if did {
				report.Removed = append(report.Removed, candidate.Digest)
			} else {
				report.Left = append(report.Left, PurgeLeft{"agents/" + candidate.Digest, "a trusted session still references this agents disk"})
			}
		}
		if len(report.Left) != 0 {
			return report, nil
		}
		if directory.Quarantine == "" {
			if err := s.clearAgentsSource(ctx, root, refs); err != nil {
				return report, err
			}
		}
		if err := s.inventoryAgentsPurge(root, &report); err != nil {
			return report, err
		}
		if len(report.Left) != 0 {
			return report, nil
		}
	}
	if err := s.purgeAgentsLeases(ctx, &report); err != nil {
		return report, err
	}
	if len(report.Left) != 0 {
		return report, nil
	}
	if err := s.finishAgentsDirectory(ctx, directory); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) clearAgentsSource(ctx context.Context, root *os.Root, refs References) error {
	receipt, err := s.readLeaseOwnership(leaseReceiptName("agents.lock"))
	if err != nil {
		return err
	}
	lease, err := s.existingReceiptLease(ctx, receipt, "agents.lock")
	if err != nil {
		return err
	}
	defer lease.Close()
	used, err := refs()
	if err != nil {
		return err
	}
	if len(used) != 0 {
		return agentsDiskError("trusted agents references remain; the newest pointer was preserved")
	}
	return s.removeAgentsSource(ctx, root)
}

func (s *Store) inventoryAgentsPurge(root *os.Root, report *PurgeReport) error {
	names, err := directoryEntries(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".lock") && supply.IsHex64(strings.TrimSuffix(name, ".lock")) {
			continue
		}
		report.Left = append(report.Left, PurgeLeft{"agents/" + name, "this remaining entry has no completed exact cleanup authority"})
	}
	return nil
}

func (s *Store) purgeAgentsLeases(ctx context.Context, report *PurgeReport) error {
	owners, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	names, err := directoryEntries(owners)
	owners.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "lease-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		owners, err := s.ownershipRoot()
		if err != nil {
			return err
		}
		raw, _, readErr := readReceipt(owners, name)
		owners.Close()
		if readErr != nil {
			return readErr
		}
		var head leaseOwnership
		if json.Unmarshal(raw, &head) != nil {
			continue
		} // unknown receipts are never swept
		role, _ := leaseRole(head.Path)
		if role != "agents-source" && role != "agents-disk" {
			continue
		}
		receipt, err := s.readLeaseOwnership(name)
		if err == nil {
			err = s.purgeLease(ctx, receipt)
		}
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{"ownership/" + name, err.Error()})
		} else {
			report.Removed = append(report.Removed, head.Path)
		}
	}
	return nil
}

func (s *Store) finishAbsentAgents(ctx context.Context, refs References, report PurgeReport) (PurgeReport, error) {
	used, err := refs()
	if err != nil {
		return report, err
	}
	if len(used) != 0 {
		report.Left = append(report.Left, PurgeLeft{"agents", "trusted agents references remain"})
		return report, nil
	}
	if err := s.purgeAgentsLeases(ctx, &report); err != nil {
		return report, err
	}
	return report, nil
}
