package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
)

// removeCorrupt is called only under the store lock, after the incoming file
// has passed the client's checks. A foreign, unsafe or multiply linked file
// is never a repair candidate. A valid existing object is never removed.
func (s *Store) removeCorrupt(item supply.Item) error {
	return s.removeCorruptContext(context.Background(), item)
}
func (s *Store) removeCorruptContext(ctx context.Context, item supply.Item) error {
	name := "sha256/" + item.SHA256
	f, err := s.openRegular(name, Mode(item))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat corrupt runtime candidate: %w", err)
	}
	checkErr := supply.CheckOpenContext(ctx, item, f)
	if checkErr == nil {
		return fmt.Errorf("runtime publication winner appeared during repair")
	}
	var mismatch *supply.MismatchError
	if !errors.As(checkErr, &mismatch) {
		return checkErr
	}
	current, err := s.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("recheck corrupt runtime candidate: %w", err)
	}
	if !os.SameFile(before, current) {
		return fmt.Errorf("corrupt runtime candidate changed before repair")
	}
	if err := s.root.Remove(name); err != nil {
		return fmt.Errorf("remove corrupt owned runtime file: %w", err)
	}
	return nil
}
