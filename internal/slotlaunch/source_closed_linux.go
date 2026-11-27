//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
)

func (s *NativeSourceV1) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	already, previous := s.closed, s.closeErr
	s.mu.Unlock()
	if already {
		return previous
	}
	if !s.PhysicalExitV1() {
		return refused("the original native slot cgroup has not proved physical closure")
	}
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	if s.closure == nil {
		proof, err := captureNativeClosureV1(s)
		if err != nil {
			s.closeErr = errors.Join(s.closeErr, err)
			s.mu.Unlock()
			return s.closeErr
		}
		s.closure = proof
	}
	if len(s.reservedDestroy) == 0 && (s.pendingLease != nil || s.reserved != nil) {
		var copies []*os.File
		for _, file := range s.files {
			copy, err := dupFile(file)
			if err != nil {
				for _, old := range copies {
					old.Close()
				}
				s.closeErr = errors.Join(s.closeErr, err)
				s.mu.Unlock()
				return s.closeErr
			}
			copies = append(copies, copy)
		}
		lease := s.pendingLease
		if lease == nil {
			lease = s.reserved.lease
		}
		copy, err := dupFile(lease)
		if err != nil {
			for _, old := range copies {
				old.Close()
			}
			s.closeErr = errors.Join(s.closeErr, err)
			s.mu.Unlock()
			return s.closeErr
		}
		s.reservedDestroy = append(copies, copy)
	}
	s.mu.Unlock()
	var result error
	s.mu.Lock()
	reservation := s.reserved
	s.mu.Unlock()
	if reservation != nil {
		result = reservation.Close()
	}
	if s.attempt != nil {
		result = errors.Join(result, s.attempt.closeJoined())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.Join(result, s.closeErr)
	}
	s.closed = true
	for _, file := range s.files {
		result = errors.Join(result, file.Close())
	}
	if s.parent != nil {
		result = errors.Join(result, s.parent.Close())
	}
	if s.guestPeer != nil {
		result = errors.Join(result, s.guestPeer.Close())
	}
	if s.pendingLease != nil {
		result = errors.Join(result, s.pendingLease.Close())
	}
	s.closeErr = errors.Join(s.closeErr, result)
	return s.closeErr
}

// ReleaseClosedEvidenceV1 is the engine's final endpoint after its actual
// positive runtime join and ordinary Source.Close. It cannot release an
// incomplete allocation or turn a cleanup error into a later nil result.
func (s *NativeSourceV1) ReleaseClosedEvidenceV1() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closure != nil && s.closure.released {
		return s.closeErr
	}
	if !s.closed || s.closure == nil || !s.closure.check() {
		return refused("the original closed source has no retained positive native closure")
	}
	if s.closeErr != nil {
		return s.closeErr
	}
	s.closeErr = errors.Join(s.closeErr, s.closure.close())
	for _, file := range s.reservedDestroy {
		s.closeErr = errors.Join(s.closeErr, file.Close())
	}
	return s.closeErr
}

// DestroyReservedDataV2 consumes only this actual closed Source's retained
// new13/SAMElease cleanup class. No ordinary fresh-EX recovery is involved.
func (s *NativeSourceV1) DestroyReservedDataV2(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	roles := append([]*os.File(nil), s.reservedDestroy...)
	s.mu.Unlock()
	if len(roles) == 0 {
		return false, nil
	}
	if len(roles) != 14 || !s.PhysicalExitV1() {
		return true, refused("the actual reserved source has no whole native closure for destruction")
	}
	operation, err := RunReservedCheckpointOperationV2(ctx, roles, CheckpointOperationV1{Session: s.request.Session, Action: "destroy"})
	if operation != nil {
		err = errors.Join(err, operation.Close())
	}
	return true, err
}
