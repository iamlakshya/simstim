package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"strings"
)

func (s *Store) recoverLeaseHeads(ctx context.Context, report *PurgeReport) error {
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil {
		return err
	}
	type candidate struct {
		name    string
		receipt leaseOwnership
		rank    int
		witness imageFileWitness
	}
	grouped := map[string][]candidate{}
	for _, name := range names {
		if !strings.HasPrefix(name, ".receipt-new-") && !strings.HasPrefix(name, ".receipt-old-") {
			continue
		}
		data, witness, err := readReceipt(root, name)
		if err != nil {
			continue
		}
		var receipt leaseOwnership
		if json.Unmarshal(data, &receipt) != nil || !receipt.known() || receipt.Store.CheckRoot(s.root) != nil || s.checkDomain(receipt.Domain) != nil {
			continue
		}
		if _, _, err := readReceipt(root, leaseReceiptName(receipt.Path)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		rank := s.leaseReplayRank(receipt)
		if rank > 0 {
			grouped[receipt.Path] = append(grouped[receipt.Path], candidate{name: name, receipt: receipt, rank: rank, witness: witness})
		}
	}
	for name, candidates := range grouped {
		if err := ctx.Err(); err != nil {
			return err
		}
		chosen := candidates[0]
		ambiguous := false
		for _, candidate := range candidates[1:] {
			if candidate.receipt.File.Identity != chosen.receipt.File.Identity {
				ambiguous = true
				break
			}
			if candidate.rank > chosen.rank {
				chosen = candidate
			}
		}
		if ambiguous {
			report.Left = append(report.Left, PurgeLeft{name, "multiple native lease generations remain in protocol backups"})
			continue
		}
		actual, exists, err := s.leaseReplayPath(chosen.receipt)
		if err != nil {
			return err
		}
		var lease *Lease
		if exists {
			lease, err = s.existingReceiptLease(ctx, chosen.receipt, actual)
			if err != nil {
				return err
			}
		}
		if s.leaseReplayRank(chosen.receipt) == 0 {
			if lease != nil {
				lease.Close()
			}
			return ownershipError("lease replay changed while waiting")
		}
		current, err := checkWitness(root, chosen.name, chosen.witness, 0o600)
		if err != nil {
			if lease != nil {
				lease.Close()
			}
			return err
		}
		current.Close()
		if err := s.writeLeaseOwnership(chosen.receipt); err != nil {
			if lease != nil {
				lease.Close()
			}
			return err
		}
		for _, candidate := range candidates {
			file, err := checkWitness(root, candidate.name, candidate.witness, 0o600)
			if err != nil {
				if lease != nil {
					lease.Close()
				}
				return err
			}
			file.Close()
			if err := root.Remove(candidate.name); err != nil {
				if lease != nil {
					lease.Close()
				}
				return err
			}
		}

		if lease != nil {
			lease.Close()
		}
	}
	return syncRoot(root)
}

func (s *Store) leaseReplayPath(receipt leaseOwnership) (string, bool, error) {
	if err := s.checkDomain(receipt.Domain); err != nil {
		return "", false, err
	}
	parent, leaf, err := s.parent(receipt.Path)
	if err != nil {
		return "", false, err
	}
	defer parent.Close()
	if err := receipt.Parent.CheckRoot(parent); err != nil {
		return "", false, err
	}
	actual := leaf
	if receipt.Quarantine != "" {
		if _, err := parent.Lstat(receipt.Quarantine); err == nil {
			if _, err := parent.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
				return "", false, ownershipError("lease replay has both original and captured names")
			}
			actual = receipt.Quarantine
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	file, err := checkWitness(parent, actual, imageFileWitness{Ref: receipt.File}, 0o600)
	if errors.Is(err, os.ErrNotExist) && receipt.Quarantine != "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	file.Close()
	relative := actual
	if directory := path.Dir(receipt.Path); directory != "." {
		relative = directory + "/" + actual
	}
	return relative, true, nil
}
func (s *Store) leaseReplayRank(receipt leaseOwnership) int {
	_, exists, err := s.leaseReplayPath(receipt)
	if err != nil {
		return 0
	}
	if receipt.Cleanup != "" {
		return 3
	}
	if receipt.Quarantine != "" {
		return 2
	}
	if exists {
		return 1
	}
	return 0
}
