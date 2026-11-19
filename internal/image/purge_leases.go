package image

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
)

func (s *Store) purgePlatformLease(ctx context.Context, name string) (*Lease, error) {
	receipt, err := s.readLeaseOwnership(leaseReceiptName(name))
	if err == nil && (receipt.Quarantine != "" || receipt.Cleanup != "") {
		actual, exists, err := s.leaseReplayPath(receipt)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, os.ErrNotExist
		}
		return s.existingReceiptLease(ctx, receipt, actual)
	}
	if err != nil && (!errors.Is(err, os.ErrNotExist) || !s.domain.IsZero()) {
		return nil, err
	}
	return s.existingLease(ctx, name, false)
}

func (s *Store) purgeOwnedLeases(ctx context.Context, report *PurgeReport) error {
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	names, err := directoryEntries(root)
	root.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "lease-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		receipt, err := s.readLeaseOwnership(name)
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{"ownership/" + name, err.Error()})
			continue
		}
		if role, _ := leaseRole(receipt.Path); role != "platform" && role != "incoming" && role != "saved-alias" && role != "saved-pending" {
			// P35 retires agents leases only after its objects and references.
			// Adding a resource role never authorises this image-only collector.
			continue
		}
		if err := s.purgeLease(ctx, receipt); err != nil {
			report.Left = append(report.Left, PurgeLeft{receipt.Path, err.Error()})
		} else {
			report.Removed = append(report.Removed, receipt.Path)
		}
	}
	return nil
}

func (s *Store) purgeLease(ctx context.Context, receipt leaseOwnership) error {
	parent, leaf, err := s.parent(receipt.Path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := receipt.Parent.CheckRoot(parent); err != nil {
		return err
	}
	actual := leaf
	if receipt.Quarantine != "" {
		_, originalErr := parent.Lstat(leaf)
		_, capturedErr := parent.Lstat(receipt.Quarantine)
		if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
			return originalErr
		}
		if capturedErr != nil && !errors.Is(capturedErr, os.ErrNotExist) {
			return capturedErr
		}
		if originalErr == nil && capturedErr == nil {
			return ownershipError("both original and captured lease remain")
		}
		if capturedErr == nil {
			actual = receipt.Quarantine
		} else if !errors.Is(capturedErr, os.ErrNotExist) {
			return capturedErr
		}
		if errors.Is(originalErr, os.ErrNotExist) && errors.Is(capturedErr, os.ErrNotExist) {
			return s.removeLeaseReceipt(receipt)
		}
	}
	relative := actual
	if directory := path.Dir(receipt.Path); directory != "." {
		relative = directory + "/" + actual
	}
	lease, err := s.existingReceiptLease(ctx, receipt, relative)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := receipt.File.CheckFile(parent, actual, lease.file, 0o600); err != nil {
		return err
	}
	if receipt.Quarantine == "" {
		receipt.Quarantine, err = randomLeaf(".lease-purge-")
		if err != nil {
			return err
		}
		if err := s.writeLeaseOwnership(receipt); err != nil {
			return err
		}
	}
	if actual == leaf {
		if err := captureLeaf(parent, leaf, receipt.Quarantine, imageFileWitness{Ref: receipt.File}, 0o600); err != nil {
			return err
		}
	}
	if err := receipt.File.CheckFile(parent, receipt.Quarantine, lease.file, 0o600); err != nil {
		return err
	}
	if err := parent.Remove(receipt.Quarantine); err != nil {
		return err
	}
	if err := syncRoot(parent); err != nil {
		return err
	}
	return s.removeLeaseReceipt(receipt)
}

func (s *Store) removeLeaseReceipt(receipt leaseOwnership) error {
	if receipt.Cleanup == "" {
		name, err := randomLeaf(".receipt-purge-")
		if err != nil {
			return err
		}
		receipt.Cleanup = name
		if err := s.writeLeaseOwnership(receipt); err != nil {
			return err
		}
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return removeReceipt(root, leaseReceiptName(receipt.Path), receipt.Cleanup)
}
