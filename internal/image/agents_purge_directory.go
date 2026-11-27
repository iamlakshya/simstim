package image

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func agentsSourceRemovalFields(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	fields, err := agentsFields(raw, "leaf", "file")
	if err != nil {
		return err
	}
	file, err := agentsFields(fields["file"], "ref", "sha256")
	if err != nil {
		return err
	}
	_, err = agentsFields(file["ref"], "version", "identity", "mode", "size", "modified", "directory")
	return err
}

func (s *Store) writeAgentsDirectory(ctx context.Context, owners *os.Root, receipt agentsDirectoryV1) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := receipt.Store.CheckRoot(s.root); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return replaceReceipt(owners, "agents-directory.json", raw)
}

func (s *Store) agentsPurgeRoot(ctx context.Context) (*os.Root, agentsDirectoryV1, error) {
	var receipt agentsDirectoryV1
	if err := ctx.Err(); err != nil {
		return nil, receipt, err
	}
	owners, err := s.ownershipRoot()
	if err != nil {
		return nil, receipt, err
	}
	defer owners.Close()
	receipt, _, err = s.readAgentsDirectory(owners, "agents-directory.json")
	if errors.Is(err, os.ErrNotExist) {
		names, listErr := directoryEntries(owners)
		if listErr != nil {
			return nil, receipt, listErr
		}
		found := false
		for _, name := range names {
			if !nonceLeaf(name, ".agents-directory-receipt-") {
				continue
			}
			candidate, _, readErr := s.readAgentsDirectory(owners, name)
			if readErr != nil {
				return nil, receipt, readErr
			}
			if candidate.Cleanup != name || candidate.Quarantine == "" || found {
				return nil, receipt, agentsDiskError("the captured agents directory authority is ambiguous")
			}
			receipt = candidate
			found = true
		}
		if !found {
			return nil, receipt, os.ErrNotExist
		}
		err = nil
	}
	if err != nil {
		return nil, receipt, err
	}
	actual := "agents"
	if receipt.Quarantine != "" {
		_, originalErr := s.root.Lstat("agents")
		_, capturedErr := s.root.Lstat(receipt.Quarantine)
		if originalErr == nil && capturedErr == nil {
			return nil, receipt, agentsDiskError("both original and captured agents directories remain")
		}
		if capturedErr == nil {
			actual = receipt.Quarantine
		} else if !errors.Is(capturedErr, os.ErrNotExist) {
			return nil, receipt, capturedErr
		}
		if errors.Is(originalErr, os.ErrNotExist) && errors.Is(capturedErr, os.ErrNotExist) {
			return nil, receipt, nil
		}
		if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
			return nil, receipt, originalErr
		}
	}
	root, err := cachefs.OpenPrivateChild(s.root, actual, false)
	if err != nil {
		return nil, receipt, err
	}
	if err := receipt.Directory.CheckRoot(root); err != nil {
		root.Close()
		return nil, receipt, err
	}
	return root, receipt, nil
}

func (s *Store) finishAgentsDirectory(ctx context.Context, receipt agentsDirectoryV1) error {
	owners, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer owners.Close()
	if receipt.Quarantine == "" {
		receipt.Quarantine, err = randomLeaf(".agents-directory-purge-")
		if err != nil {
			return err
		}
		if err := s.writeAgentsDirectory(ctx, owners, receipt); err != nil {
			return err
		}
	}
	root, live, err := s.agentsPurgeRoot(ctx)
	if err != nil {
		return err
	}
	if root != nil {
		defer root.Close()
		names, err := directoryEntries(root)
		if err != nil {
			return err
		}
		if len(names) != 0 {
			return agentsDiskError("the owned agents directory still contains entries; they were preserved")
		}
		if err := live.Directory.CheckRoot(root); err != nil {
			return err
		}
		if _, err := s.root.Lstat("agents"); err == nil {
			if s.captureHook != nil {
				s.captureHook(true, s.root, "agents", live.Quarantine)
			}
			if err := cachefs.RenameNoReplace(s.root, "agents", live.Quarantine); err != nil {
				return err
			}
			if s.captureHook != nil {
				s.captureHook(false, s.root, "agents", live.Quarantine)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		captured, err := cachefs.OpenPrivateChild(s.root, live.Quarantine, false)
		if err != nil {
			return err
		}
		checkErr := live.Directory.CheckRoot(captured)
		if checkErr == nil {
			names, checkErr = directoryEntries(captured)
			if checkErr == nil && len(names) != 0 {
				checkErr = agentsDiskError("a captured agents directory gained an unknown entry")
			}
		}
		checkErr = errors.Join(checkErr, captured.Close())
		if checkErr != nil {
			return errors.Join(checkErr, cachefs.RenameNoReplace(s.root, live.Quarantine, "agents"))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.root.Remove(live.Quarantine); err != nil {
			return err
		}
		if err := s.syncDir("."); err != nil {
			return err
		}
	}
	receipt = live
	if receipt.Cleanup == "" {
		receipt.Cleanup, err = randomLeaf(".agents-directory-receipt-")
		if err != nil {
			return err
		}
		if err := s.writeAgentsDirectory(ctx, owners, receipt); err != nil {
			return err
		}
	}
	return removeReceipt(owners, "agents-directory.json", receipt.Cleanup)
}
