package image

import (
	"context"
	"errors"
	"os"
)

func (s *Store) removeAgentsSource(ctx context.Context, root *os.Root) error {
	owners, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer owners.Close()
	directory, _, err := s.readAgentsDirectory(owners, "agents-directory.json")
	if err != nil {
		return err
	}
	if directory.Source == nil {
		if _, err := s.readAgentsSource(root); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		_, witness, err := readReceipt(root, "source.json")
		if err != nil {
			return err
		}
		leaf, err := randomLeaf(".agents-source-purge-")
		if err != nil {
			return err
		}
		directory.Source = &agentsSourceRemovalV1{Leaf: leaf, File: witness}
		if err := s.writeAgentsDirectory(ctx, owners, directory); err != nil {
			return err
		}
	}
	intent := directory.Source
	_, originalErr := root.Lstat("source.json")
	_, capturedErr := root.Lstat(intent.Leaf)
	if originalErr == nil && capturedErr == nil {
		return agentsDiskError("both original and captured newest agents pointers remain")
	}
	if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
		return originalErr
	}
	if capturedErr != nil && !errors.Is(capturedErr, os.ErrNotExist) {
		return capturedErr
	}
	if originalErr == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := captureLeaf(root, "source.json", intent.Leaf, intent.File, 0o600); err != nil {
			return err
		}
		capturedErr = nil
	}
	if capturedErr == nil {
		file, err := checkWitness(root, intent.Leaf, intent.File, 0o600)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(intent.Leaf); err != nil {
			return err
		}
		if err := syncRoot(root); err != nil {
			return err
		}
	}
	directory.Source = nil
	return s.writeAgentsDirectory(ctx, owners, directory)
}
