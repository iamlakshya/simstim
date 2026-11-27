package files

import (
	"errors"
	"fmt"
	"os"
	"path"
)

var sidecars = []string{"", "-wal", "-shm", "-journal"}

// CopyFamilies copies complete stopped file families into an empty root.
// When the main file is absent, the whole family is absent in this epoch.
// The caller stops all writers first. WAL is copied with its database, so
// copying never combines database and journal files from different starts.
func CopyFamilies(src, dst *os.Root, names []string) error {
	if err := (StatePlan{Files: names}).Validate(); err != nil {
		return err
	}
	for _, name := range names {
		main, err := OpenRegular(src, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open state file %s: %w", name, err)
		}
		main.Close()
		for _, suffix := range sidecars {
			err = copyRegular(src, dst, name+suffix)
			if suffix != "" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("copy state file %s: %w", name+suffix, err)
			}
		}
	}
	return syncRoot(dst)
}

// RestoreFamilies restores one read-only committed generation before the
// first agent starts. The fresh home is inaccessible to agent processes.
func RestoreFamilies(home, restore string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	src, err := os.OpenRoot(restore)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer dst.Close()
	for _, name := range names {
		if !Relative(name) || forbiddenState(name) {
			return fmt.Errorf("unsafe state file path")
		}
		if err = dst.MkdirAll(path.Dir(name), 0700); err != nil {
			return err
		}
		for _, suffix := range sidecars {
			if err = dst.Remove(name + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return CopyFamilies(src, dst, names)
}

// CaptureFamilies writes only the next incoming generation after all guest
// children have ended. Publication remains the supervisor's responsibility.
func CaptureFamilies(home, incoming string, names []string) error {
	src, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(incoming)
	if err != nil {
		return err
	}
	defer dst.Close()
	// The agent may have filled the incoming area while it ran. Replace it
	// through the pinned root, then copy only the declared paths.
	entries, err := dst.Open(".")
	if err != nil {
		return err
	}
	items, err := entries.ReadDir(-1)
	entries.Close()
	if err != nil {
		return err
	}
	for _, e := range items {
		if err = dst.RemoveAll(e.Name()); err != nil {
			return err
		}
	}
	return CopyFamilies(src, dst, names)
}
