package image

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// removeAgentsRecord runs under exclusive disk/source ownership. The durable
// closed capture intent precedes either rename, and data disappears before its
// authority record. Each unlink uses the canonical native capture witness.
func (s *Store) removeAgentsRecord(ctx context.Context, root *os.Root, metadataName string, record agentsRecordV1, metadata imageFileWitness) error {
	if record.Purge == nil {
		if metadataName != record.Disk.Content.SHA256+".json" {
			return agentsDiskError("the agents metadata is not its original committed record")
		}
		if err := checkAgentsRecordWitness(root, record); err != nil {
			return err
		}
		data, err := randomLeaf(".agents-data-purge-")
		if err != nil {
			return err
		}
		meta, err := randomLeaf(".agents-meta-purge-")
		if err != nil {
			return err
		}
		record.Purge = &agentsPurgeV1{Data: data, Metadata: meta}
		if err := writeAgentsRecord(root, record); err != nil {
			return err
		}
		_, metadata, err = s.readAgentsRecord(root, record.Disk.Content.SHA256)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := removeAgentsData(root, record); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if metadataName != record.Purge.Metadata {
		if _, err := root.Lstat(record.Purge.Metadata); !errors.Is(err, os.ErrNotExist) {
			return agentsDiskError("the agents metadata capture collides with another entry")
		}
		if err := captureLeaf(root, metadataName, record.Purge.Metadata, metadata, 0o600); err != nil {
			return err
		}
	}
	file, err := checkWitness(root, record.Purge.Metadata, metadata, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Remove(record.Purge.Metadata); err != nil {
		return err
	}
	return syncRoot(root)
}

func removeAgentsData(root *os.Root, record agentsRecordV1) error {
	quarantine := record.Purge.Data
	_, capturedErr := root.Lstat(quarantine)
	if capturedErr != nil && !errors.Is(capturedErr, os.ErrNotExist) {
		return capturedErr
	}
	if capturedErr == nil {
		for _, name := range []string{record.Leaf, record.Disk.Content.SHA256 + ".img"} {
			if name == quarantine {
				continue
			}
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return agentsDiskError("both original and captured agents bytes remain; all were preserved")
			}
		}
	} else {
		leaf, err := agentsRecordLeaf(root, record)
		if err != nil {
			// A persisted removal intent may already have unlinked the data.
			// Absence of BOTH original closed names grants no other unlink.
			for _, name := range []string{record.Leaf, record.Disk.Content.SHA256 + ".img"} {
				if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
					return agentsDiskError("agents removal has an unsafe remaining data name")
				}
			}
			return nil
		}
		if err := captureLeaf(root, leaf, quarantine, imageFileWitness{Ref: record.File}, 0o444); err != nil {
			return err
		}
	}
	file, err := cachefs.OpenFile(root, quarantine, os.O_RDONLY, 0o444)
	if err != nil {
		return err
	}
	err = errors.Join(record.File.CheckFile(root, quarantine, file, 0o444), file.Close())
	if err != nil {
		return err
	}
	if err := root.Remove(quarantine); err != nil {
		return err
	}
	return syncRoot(root)
}
