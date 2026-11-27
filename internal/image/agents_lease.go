package image

import (
	"context"
	"errors"
	"os"
	"strings"
)

func (s *Store) permitAgentsLeaseCreation(ctx context.Context, name string) error {
	if err := s.domain.Validate(); err != nil {
		return err
	}
	var names []string
	if name == "agents.lock" {
		names = []string{"agents", "ownership/agents-directory.json"}
	} else {
		root, err := s.agentsRoot(ctx, false)
		if err != nil {
			return err
		}
		if err := root.Close(); err != nil {
			return err
		}
		key := strings.TrimSuffix(name, ".lock")
		names = []string{key + ".img", key + ".json"}
	}
	for _, leaf := range names {
		if _, err := s.root.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return imageDomainError("an existing agents object cannot acquire a replacement lease generation")
		}
	}
	return nil
}

func (s *Store) checkAgentsLeaseObject(ctx context.Context, name string) error {
	if err := s.domain.Validate(); err != nil {
		return err
	}
	if name == "agents.lock" {
		_, directoryErr := s.root.Lstat("agents")
		_, receiptErr := s.root.Lstat("ownership/agents-directory.json")
		if errors.Is(directoryErr, os.ErrNotExist) && errors.Is(receiptErr, os.ErrNotExist) {
			return nil // only the native source-lease creator may create its child
		}
	}
	root, err := s.agentsRoot(ctx, false)
	if err != nil {
		return err
	}
	defer root.Close()
	if name == "agents.lock" {
		return nil
	}
	key := strings.TrimSuffix(strings.TrimPrefix(name, "agents/"), ".lock")
	_, fileErr := root.Lstat(key + ".img")
	_, receiptErr := root.Lstat(key + ".json")
	if errors.Is(fileErr, os.ErrNotExist) && errors.Is(receiptErr, os.ErrNotExist) {
		return nil
	}
	_, _, err = s.readAgentsRecord(root, key)
	return err
}
