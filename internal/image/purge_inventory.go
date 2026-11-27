package image

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
)

func (s *Store) inventoryUnowned(report *PurgeReport) {
	seen := map[string]bool{}
	for _, left := range report.Left {
		seen[left.Path] = true
	}
	add := func(name, reason string) {
		if !seen[name] {
			report.Left = append(report.Left, PurgeLeft{name, reason})
			seen[name] = true
		}
	}
	names, err := directoryEntries(s.root)
	if err != nil {
		add(".", "the anchored store inventory cannot be read")
		return
	}
	for _, name := range names {
		if name != "incoming" && name != "ownership" {
			add(name, "a remaining entry has no completed exact cleanup receipt")
			continue
		}
		root, err := s.childRoot(name)
		if err != nil {
			add(name, "the infrastructure directory is unsafe")
			continue
		}
		children, err := directoryEntries(root)
		root.Close()
		if err != nil {
			add(name, "the infrastructure inventory cannot be read")
			continue
		}
		for _, child := range children {
			add(name+"/"+child, "a remaining part, marker or file is not authorised for cleanup")
		}
	}
	sort.Slice(report.Left, func(i, j int) bool { return report.Left[i].Path < report.Left[j].Path })
	sort.Strings(report.Removed)
}

// recoverMovedReceipt finishes only a self-described removal marker whose
// payload paths are already absent. Data deletion still requires its existing
// exclusive lease. This protocol-only replay never recreates a lease domain.
func (s *Store) recoverMovedReceipt(name string) error {
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, witness, err := readReceipt(root, name)
	if err != nil {
		return err
	}
	var image imageOwnership
	if json.Unmarshal(data, &image) == nil && image.known() && image.Cleanup == name && image.Purge != nil {
		if err := s.checkDomain(image.Domain); err != nil {
			return err
		}
		if err := image.Store.CheckRoot(s.root); err != nil {
			return err
		}
		for _, leaf := range []string{image.Leaf, image.Purge.Quarantine, image.Digest[7:]} {
			if _, err := s.root.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
				return ownershipError("a moved receipt still names image data")
			}
		}
	} else {
		var lease leaseOwnership
		if json.Unmarshal(data, &lease) != nil || !lease.known() || lease.Cleanup != name || lease.Quarantine == "" {
			return ownershipError("moved receipt does not describe a completed owned removal")
		}
		if err := s.checkDomain(lease.Domain); err != nil {
			return err
		}
		if err := lease.Store.CheckRoot(s.root); err != nil {
			return err
		}
		parent, leaf, err := s.parent(lease.Path)
		if err != nil {
			return err
		}
		defer parent.Close()
		if err := lease.Parent.CheckRoot(parent); err != nil {
			return err
		}
		for _, name := range []string{leaf, lease.Quarantine} {
			if _, err := parent.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return ownershipError("a moved receipt still names a lease")
			}
		}
	}
	file, err := checkWitness(root, name, witness, 0o600)
	if err != nil {
		return err
	}
	file.Close()
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncRoot(root)
}

func (s *Store) recoverMovedReceipts(report *PurgeReport) {
	root, err := s.ownershipRoot()
	if err != nil {
		return
	}
	names, err := directoryEntries(root)
	root.Close()
	if err != nil {
		return
	}
	for _, name := range names {
		if strings.HasPrefix(name, ".receipt-purge-") {
			if err := s.recoverMovedReceipt(name); err != nil {
				report.Left = append(report.Left, PurgeLeft{"ownership/" + name, err.Error()})
			}
		}
	}
}
