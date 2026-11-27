package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

type agentsDirectoryV1 struct {
	Version    int                    `json:"version"`
	Domain     *cachefs.Domain        `json:"domain"`
	Store      cachefs.Reference      `json:"store"`
	Directory  cachefs.Reference      `json:"directory"`
	Quarantine string                 `json:"quarantine"`
	Cleanup    string                 `json:"cleanup"`
	Source     *agentsSourceRemovalV1 `json:"source"`
}

type agentsSourceRemovalV1 struct {
	Leaf string           `json:"leaf"`
	File imageFileWitness `json:"file"`
}

// agentsRoot runs while its caller owns agents.lock when create is true.
// An existing unreceipted child is preserved even if it appears empty.
func (s *Store) agentsRoot(ctx context.Context, create bool) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.domain.Validate(); err != nil {
		return nil, err
	}
	if err := s.generation.CheckRoot(s.root); err != nil {
		return nil, err
	}
	owners, err := s.ownershipRoot()
	if err != nil {
		return nil, err
	}
	defer owners.Close()
	receipt, _, err := s.readAgentsDirectory(owners, "agents-directory.json")
	if errors.Is(err, os.ErrNotExist) && create {
		return s.createAgentsRoot(ctx, owners)
	}
	if err != nil {
		return nil, err
	}
	if receipt.Quarantine != "" || receipt.Cleanup != "" || receipt.Source != nil {
		return nil, agentsDiskError("the agents directory has a pending owned removal")
	}
	root, err := cachefs.OpenPrivateChild(s.root, "agents", false)
	if err != nil {
		return nil, err
	}
	if err := receipt.Directory.CheckRoot(root); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func (s *Store) readAgentsDirectory(owners *os.Root, name string) (agentsDirectoryV1, imageFileWitness, error) {
	var receipt agentsDirectoryV1
	raw, witness, err := readReceipt(owners, name)
	if err != nil {
		return receipt, witness, err
	}
	fields, err := agentsFields(raw, "version", "domain", "store", "directory", "quarantine", "cleanup", "source")
	if err != nil {
		return receipt, witness, err
	}
	if err := agentsRootFields(fields); err != nil {
		return receipt, witness, err
	}
	if err := agentsSourceRemovalFields(fields["source"]); err != nil {
		return receipt, witness, err
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.Domain == nil || *receipt.Domain != s.domain || !agentsRootReference(receipt.Store) || !agentsRootReference(receipt.Directory) || receipt.Quarantine != "" && !nonceLeaf(receipt.Quarantine, ".agents-directory-purge-") || receipt.Cleanup != "" && !nonceLeaf(receipt.Cleanup, ".agents-directory-receipt-") {
		return receipt, witness, agentsDiskError("the agents directory has unknown or different ownership")
	}
	if receipt.Source != nil && (!nonceLeaf(receipt.Source.Leaf, ".agents-source-purge-") || !receipt.Source.File.Ref.Known() || receipt.Source.File.Ref.Directory || receipt.Source.File.Ref.Mode != 0o600 || !supply.IsHex64(receipt.Source.File.SHA256) || len(receipt.Source.File.extra) != 0) {
		return receipt, witness, agentsDiskError("the agents source cleanup has unknown native ownership")
	}
	return receipt, witness, receipt.Store.CheckRoot(s.root)
}

func (s *Store) createAgentsRoot(ctx context.Context, owners *os.Root) (root *os.Root, returned error) {
	returned = s.withPublication(ctx, func() error {
		var err error
		root, err = s.exclusiveDirectory("agents")
		if err != nil {
			return agentsDiskError("the agents child already exists without a canonical receipt; it was preserved")
		}
		directory, err := cachefs.CaptureRoot(root)
		if err != nil {
			return err
		}
		domain := s.domain
		raw, err := json.Marshal(agentsDirectoryV1{Version: 1, Domain: &domain, Store: s.generation, Directory: directory})
		if err != nil {
			return err
		}
		if err := syncRoot(root); err != nil {
			return err
		}
		if err := replaceReceipt(owners, "agents-directory.json", raw); err != nil {
			return err
		}
		return s.syncDir(".")
	})
	if returned != nil && root != nil {
		root.Close()
		root = nil
	}
	return root, returned
}

func (s *Store) readAgentsSource(root *os.Root) (agentsSourceV1, error) {
	var source agentsSourceV1
	raw, _, err := readReceipt(root, "source.json")
	if err != nil {
		return source, err
	}
	source, err = decodeAgentsSource(raw, s.domain)
	if err != nil {
		return source, err
	}
	if err := source.Store.CheckRoot(s.root); err != nil {
		return source, err
	}
	return source, source.Directory.CheckRoot(root)
}

func (s *Store) readAgentsRecord(root *os.Root, digest string) (agentsRecordV1, imageFileWitness, error) {
	return s.readAgentsRecordAt(root, digest+".json", digest)
}

func (s *Store) readAgentsRecordAt(root *os.Root, name, digest string) (agentsRecordV1, imageFileWitness, error) {
	var record agentsRecordV1
	raw, metadata, err := readReceipt(root, name)
	if err != nil {
		return record, metadata, err
	}
	record, err = decodeAgentsRecord(raw, s.domain, digest)
	if err != nil {
		return record, metadata, err
	}
	if err := record.Store.CheckRoot(s.root); err != nil {
		return record, metadata, err
	}
	return record, metadata, record.Directory.CheckRoot(root)
}

// OpenAgentsV1 opens exact canonical-producer bytes under their existing shared
// disk lease. It creates nothing and grants no runtime/image pin authority.
// The caller retains the lease through protected metadata publication and use.
func (s *Store) OpenAgentsV1(ctx context.Context, expected supply.ContentRef) (file *os.File, lease *Lease, returned error) {
	if err := ValidateAgentsContentV1(expected); err != nil {
		return nil, nil, err
	}
	lease, err := s.existingLease(ctx, "agents/"+expected.SHA256+".lock", true)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if returned != nil {
			if file != nil {
				file.Close()
			}
			lease.Close()
		}
	}()
	root, err := s.agentsRoot(ctx, false)
	if err != nil {
		return nil, lease, err
	}
	defer root.Close()
	record, _, err := s.readAgentsRecord(root, expected.SHA256)
	if err != nil {
		return nil, lease, err
	}
	if record.Purge != nil || record.Leaf != expected.SHA256+".img" || record.Disk.Content != expected {
		return nil, lease, agentsDiskError("the expected agents disk is not a completed exact producer publication")
	}
	file, err = cachefs.OpenFile(root, record.Leaf, os.O_RDONLY, 0o444)
	if err != nil {
		return nil, lease, err
	}
	if err := record.File.CheckFile(root, record.Leaf, file, 0o444); err != nil {
		return file, lease, err
	}
	if err := supply.CheckContent(expected, file); err != nil {
		return file, lease, err
	}
	if _, err := InspectAgentsV1(file); err != nil {
		return file, lease, err
	}
	return file, lease, record.File.CheckFile(root, record.Leaf, file, 0o444)
}
