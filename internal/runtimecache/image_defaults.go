package runtimecache

import (
	"context"
	"fmt"
	"slices"

	"github.com/iamlakshya/simstim/internal/supply"
)

// ImageDefaultsUnavailableError refuses image GC when selected label records
// do not carry authoritative client metadata. Callers may request a refresh.
type ImageDefaultsUnavailableError struct {
	Runtime string
	Err     error
}

// Error describes the missing or malformed retention authority.
func (e *ImageDefaultsUnavailableError) Error() string {
	if e.Runtime != "" {
		return fmt.Sprintf("runtime %s needs client image-default metadata refresh", e.Runtime)
	}
	if e.Err != nil {
		return "runtime image-default metadata is unavailable: " + e.Err.Error()
	}
	return "runtime label metadata needs a client refresh before image GC"
}

// Unwrap preserves the cause of malformed or unreadable metadata.
func (e *ImageDefaultsUnavailableError) Unwrap() error { return e.Err }

func cloneImageDefaults(defaults *[]supply.Item) *[]supply.Item {
	if defaults == nil {
		return nil
	}
	cloned := slices.Clone(*defaults)
	if len(cloned) == 0 {
		cloned = []supply.Item{}
	}
	return &cloned
}

// ImageDefaults returns validated default-image references from the two
// newest runtime labels under the store transaction. They are retention
// metadata and must never become execution pins supplied by a host.
// Image GC must call again while holding its exclusive image lease.
func (s *Store) ImageDefaults(ctx context.Context, arch string) ([]supply.Item, error) {
	images := []supply.Item{}
	err := s.locked(ctx, func() error {
		records, err := s.recordsContext(ctx, arch)
		if err != nil {
			return &ImageDefaultsUnavailableError{Err: err}
		}
		if len(records) == 0 {
			return &ImageDefaultsUnavailableError{}
		}
		for _, record := range records[:min(2, len(records))] {
			if record.ImageDefaults == nil {
				return &ImageDefaultsUnavailableError{Runtime: record.Manifest.Runtime}
			}
			images = append(images, (*record.ImageDefaults)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return images, nil
}
