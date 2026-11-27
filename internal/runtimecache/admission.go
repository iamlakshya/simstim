package runtimecache

import "fmt"

// AdmissionError reports that this transfer's signed size exceeds the free
// disk space observed before creating its part. It imposes no new file cap.
type AdmissionError struct{ Needed, Available uint64 }

func (e *AdmissionError) Error() string {
	return fmt.Sprintf("runtime file needs %d bytes but the cache has %d bytes free", e.Needed, e.Available)
}

func (s *Store) admit(size int64) error {
	dir, err := s.root.Open("incoming")
	if err != nil {
		return fmt.Errorf("open runtime incoming directory for admission: %w", err)
	}
	defer dir.Close()
	available, err := availableDisk(dir)
	if err != nil {
		return fmt.Errorf("check runtime cache free space: %w", err)
	}
	if uint64(size) > available {
		return &AdmissionError{Needed: uint64(size), Available: available}
	}
	return nil
}
