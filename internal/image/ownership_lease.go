package image

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (s *Store) recordLease(name string, file *os.File) error {
	parent, leaf, err := s.parent(name)
	if err != nil {
		return err
	}
	defer parent.Close()
	reference, err := cachefs.CaptureFile(parent, leaf, file, 0o600)
	if err != nil {
		return err
	}
	directory, err := cachefs.CaptureRoot(parent)
	if err != nil {
		return err
	}
	receipt := leaseOwnership{Version: 1, Domain: s.receiptDomain(), Path: name, Store: s.generation, Parent: directory, File: reference}
	if !receipt.known() {
		return ownershipError("lease creator has no known store or native witness")
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if _, _, err := readReceipt(root, leaseReceiptName(name)); err == nil {
		return ownershipError("an earlier lease receipt remains and cannot be adopted")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return replaceReceipt(root, leaseReceiptName(name), data)
}

func (s *Store) readLeaseOwnership(name string) (leaseOwnership, error) {
	var receipt leaseOwnership
	root, err := s.ownershipRoot()
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	data, _, err := readReceipt(root, name)
	if err != nil {
		return receipt, err
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return receipt, err
	}
	if !receipt.known() || leaseReceiptName(receipt.Path) != name {
		return receipt, ownershipError("lease receipt has unknown fields or witnesses")
	}
	if err := s.checkDomain(receipt.Domain); err != nil {
		return receipt, err
	}
	if err := receipt.Store.CheckRoot(s.root); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func (s *Store) writeLeaseOwnership(receipt leaseOwnership) error {
	if err := s.checkDomain(receipt.Domain); err != nil {
		return err
	}
	if !receipt.known() {
		return ownershipError("lease receipt is not authoritative")
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return replaceReceipt(root, leaseReceiptName(receipt.Path), data)
}
