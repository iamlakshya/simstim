package image

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Cached checks and marks a stored root as used under its exclusive lease.
// It never fetches registry bytes or starts a converter.
func (s *Store) Cached(ctx context.Context, request Request) (CacheStatus, error) {
	if err := s.requireWritableHost(); err != nil {
		return CacheStatus{}, err
	}
	if err := request.Validate(); err != nil {
		return CacheStatus{}, err
	}
	if err := s.checkSavedRequestDomain(request); err != nil {
		return CacheStatus{}, err
	}
	if request.Transfer != "" {
		return CacheStatus{}, badLayer("a cached image check cannot name a transfer")
	}
	lease, err := s.lock(ctx, request.PlatformDigest[7:]+".lock", false)
	if err != nil {
		return CacheStatus{}, err
	}
	defer lease.Close()
	record, err := s.existingOwned(ctx, request)
	if errors.Is(err, os.ErrNotExist) {
		return CacheStatus{}, nil
	}
	if err != nil {
		return CacheStatus{}, err
	}
	return CacheStatus{Present: true, Image: &record}, nil
}

// OpenImage returns a checked read-only descriptor and a shared platform lease.
// A start keeps the lease until its trusted session manifest names the image.
// The caller closes both results. The file descriptor stays valid after the
// lease closes; session references then protect it from collection.
func (s *Store) OpenImage(ctx context.Context, request Request) (file *os.File, lease *Lease, record Record, returned error) {
	if err := request.Validate(); err != nil {
		return nil, nil, record, err
	}
	status, err := s.Cached(ctx, request)
	if err != nil {
		return nil, nil, record, err
	}
	if !status.Present {
		return nil, nil, record, fmt.Errorf("open prepared image: %w", os.ErrNotExist)
	}
	return s.OpenImageExisting(ctx, request)
}

// OpenImageExisting checks existing exact client-authorised image bytes under
// their existing shared lease. It creates and updates nothing; canonical host
// domain/receipt checks still run after native ownership is acquired.
func (s *Store) OpenImageExisting(ctx context.Context, request Request) (file *os.File, lease *Lease, record Record, returned error) {
	if err := request.Validate(); err != nil {
		return nil, nil, record, err
	}
	if err := s.checkSavedRequestDomain(request); err != nil {
		return nil, nil, record, err
	}
	if request.Transfer != "" {
		return nil, nil, record, badLayer("an existing image open cannot name a transfer")
	}
	held, err := s.existingLease(ctx, request.PlatformDigest[7:]+".lock", true)
	if err != nil {
		return nil, nil, record, err
	}
	defer func() {
		if returned != nil {
			if file != nil {
				file.Close()
			}
			held.Close()
			file = nil
			lease = nil
		}
	}()
	record, err = s.readRecord(request.PlatformDigest[7:])
	if err != nil {
		return nil, nil, record, err
	}
	if record.Arch != request.Arch {
		return nil, nil, record, badLayer("stored image architecture differs from the client request")
	}
	record, err = projectSavedSelection(request, record)
	if err != nil {
		return nil, nil, record, err
	}
	if err := rootCheck(request, record); err != nil {
		return nil, nil, record, err
	}
	record.Ref = request.Ref
	record.Digest = request.Digest
	file, err = s.regular(request.PlatformDigest[7:]+"/rootfs.img", os.O_RDONLY, 0o444)
	if err != nil {
		return nil, nil, record, err
	}
	info, err := Inspect(file)
	if err != nil {
		return file, nil, record, err
	}
	if err := s.checkSavedDescriptor(ctx, record, file); err != nil {
		return file, nil, record, err
	}
	if info.RootHash != record.RootHash || info.DataBlocks != record.DataBlocks || info.Size != record.Size {
		return file, nil, record, mismatch("opened image descriptor")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return file, nil, record, fmt.Errorf("rewind checked image: %w", err)
	}
	return file, held, record, nil
}
