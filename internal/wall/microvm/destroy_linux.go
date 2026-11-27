//go:build linux

package microvm

import (
	"context"
	"fmt"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"os"
	"time"
)

func (s *sandbox) Destroy() error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()
	s.mu.Lock()
	if s.destroyed {
		s.mu.Unlock()
		return nil
	}
	if s.fileCloseErr != nil {
		err := s.fileCloseErr
		s.mu.Unlock()
		return err
	}
	s.destroying = true
	s.preparationClosing = true
	removed := s.dataRemoved
	released := s.preparedReleased
	started := s.started
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.destroying = false; s.mu.Unlock() }()
	if started {
		_ = s.Kill()
		s.Wait()
		if !s.refreshPhysicalExit() {
			return fmt.Errorf("the owned microvm may still run. Retain its disk and retry cleanup through this same controller")
		}
	}
	if err := s.closeSnapshotDiskV1(); err != nil {
		return err
	}
	if err := s.releaseBootBorrowersV2(); err != nil {
		return err
	}
	if !removed {
		if released {
			return fmt.Errorf("the released preparation no longer owns destructive disk custody; Try: use the actual current session owner")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.mu.Lock()
		root, rootErr := s.preparedRoot, s.rootCloseErr
		s.mu.Unlock()
		if rootErr != nil {
			return rootErr
		}
		if root == nil {
			var err error
			root, err = cachefs.OpenPrivateRoot(s.spec.SessionDir, false)
			if err != nil {
				return err
			}
			s.mu.Lock()
			s.preparedRoot = root
			s.mu.Unlock()
		}
		s.mu.Lock()
		lease, leaseErr := s.lease, s.leaseCloseErr
		s.mu.Unlock()
		if leaseErr != nil {
			return leaseErr
		}
		if lease == nil {
			var err error
			lease, err = acquireVMLeaseFor(ctx, root, s.driver.kind)
			s.mu.Lock()
			s.lease = lease
			s.mu.Unlock()
			if err != nil {
				return err
			}
		} else if err := lockVMLeaseForV1(ctx, root, s.driver.kind, lease); err != nil {
			return err
		}
		if s.uid == slotUIDModeV1Name {
			if err := s.destroySlotDataV1(ctx); err != nil {
				return err
			}
		}
		if err := jail.RemoveAll(ctx, vmDataDir(s.spec)); err != nil {
			return err
		}
		if err := root.Remove(s.driver.kind.name() + ".json"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove microvm specification: %w", err)
		}

		if s.driver.kind.isFirecracker() {
			file, err := cachefs.OpenFile(root, firecrackerConfigName, os.O_RDONLY, 0o444)
			if err == nil {
				if err := file.Close(); err != nil {
					return err
				}
				if err := root.Remove(firecrackerConfigName); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}

		s.mu.Lock()
		s.dataRemoved = true
		s.mu.Unlock()
	}
	if err := s.releaseOriginalEvidenceV1(); err != nil {
		return err
	}
	if err := s.releaseLease(); err != nil {
		return err
	}
	if err := s.closePreparationRootV1(); err != nil {
		return err
	}
	s.mu.Lock()
	s.destroyed = true
	s.mu.Unlock()
	return nil
}
