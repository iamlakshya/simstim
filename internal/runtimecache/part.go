package runtimecache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Part owns one exclusively created partial file. No other transfer may
// write it, publish it or remove it. A failed write closes only this part.
type Part struct {
	publicationCloseAttempted                         bool
	publicationClose                                  supply.FileCloseFactsV1
	incomingSync                                      *os.File
	incomingDataSynced, incomingSyncCloseAttempted    bool
	incomingSyncClose                                 supply.FileCloseFactsV1
	reader                                            *os.File
	writerAttempted, readerAttempted, readerReleased  bool
	writerClose, readerClose                          supply.FileCloseFactsV1
	ioFailure                                         error
	mu                                                sync.Mutex
	store                                             *Store
	incoming                                          *os.Root
	basename                                          string
	parentReleased                                    bool
	publicationDir                                    *os.File
	linked, publicationSynced, publicationDirReleased bool
	file                                              *os.File
	item                                              supply.Item
	name                                              string
	receiver                                          *supply.Receiver
	started                                           time.Time
	closed                                            bool
	closing                                           bool
	removed                                           bool
	incomingSynced                                    bool
	writerReleased                                    bool
}

// Begin creates one new part for item. Manifest validation is required before
// this call. The file is never opened by a name supplied by a host or caller.
func (s *Store) Begin(item supply.Item) (*Part, error) {
	if !s.domain.IsZero() {
		return nil, runtimeDomainError("host runtime transfer requires its explicit operation context")
	}
	return s.BeginContext(context.Background(), item)
}

// BeginContext validates the exact store domain under its stable transaction
// before creating this operation's own part. Host callers retain their active
// publisher and short placement gate around this namespace change.
func (s *Store) BeginContext(ctx context.Context, item supply.Item) (*Part, error) {
	var part *Part
	err := s.withPublication(ctx, func() error {
		return s.locked(ctx, func() error {
			var err error
			part, err = s.beginPart(item)
			return err
		})
	})
	return part, err
}

func (s *Store) beginPart(item supply.Item) (*Part, error) {
	if s.readOnly {
		return nil, fmt.Errorf("cannot begin a transfer in a read-only runtime store")
	}
	if !supply.IsHex64(item.SHA256) || !IsRuntime(item.Kind) || item.Size <= 0 {
		return nil, fmt.Errorf("runtime item has invalid metadata")
	}
	if err := s.admit(item.Size); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("name runtime part: %w", err)
	}
	basename := item.SHA256 + "." + hex.EncodeToString(nonce[:]) + ".part"
	incoming, err := cachefs.OpenPrivateChild(s.root, "incoming", false)
	if err != nil {
		return nil, fmt.Errorf("retain original runtime incoming directory: %w", err)
	}
	part := &Part{store: s, incoming: incoming, item: item, name: "incoming/" + basename, basename: basename, started: time.Now(), removed: true, incomingSynced: true, writerReleased: true}
	f, err := openNoFollow(incoming, basename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if cleanup := part.Close(); cleanup != nil {
			return part, errors.Join(fmt.Errorf("create runtime part: %w", err), cleanup)
		}
		return nil, fmt.Errorf("create runtime part: %w", err)
	}
	part.file, part.removed, part.incomingSynced, part.writerReleased = f, false, false, false
	st, err := f.Stat()
	if err != nil || !descriptorSafe(f, st) || !modeMatches(st, 0o600) {
		cleanup := part.Close()
		if cleanup != nil {
			return part, errors.Join(fmt.Errorf("new runtime part does not have private ownership"), cleanup)
		}
		return nil, fmt.Errorf("new runtime part does not have private ownership")
	}
	receiver, err := supply.NewReceiver(item, f)
	if err != nil {
		cleanup := part.Close()
		if cleanup != nil {
			return part, errors.Join(fmt.Errorf("prepare runtime receiver: %w", err), cleanup)
		}
		return nil, fmt.Errorf("prepare runtime receiver: %w", err)
	}
	part.receiver = receiver
	return part, nil
}

// Digest returns this transfer's client-authorised digest.
func (p *Part) Digest() string { return p.item.SHA256 }

// Progress returns the count and start time of this part without its path.
func (p *Part) Progress() (int64, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.receiver == nil {
		return 0, p.started
	}
	return p.receiver.Received(), p.started
}

// WriteAt appends one bounded chunk at the exact next offset. A replay or
// over-size chunk deletes this part before any of that chunk is written.
func (p *Part) WriteAt(data []byte, offset int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.closing {
		return fmt.Errorf("runtime transfer is closed")
	}
	if err := p.receiver.WriteAt(data, offset); err != nil {
		p.ioFailure = errors.Join(p.ioFailure, err)
		return errors.Join(fmt.Errorf("receive runtime chunk: %w", err), p.closeLocked())
	}
	return nil
}

// Finish verifies this part, then atomically publishes it without replacing
// a checked existing winner. It removes only its own incoming file.
func (p *Part) Finish(ctx context.Context) (returned error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.closing || p.ioFailure != nil {
		return fmt.Errorf("runtime transfer is closed or has an original IO failure")
	}
	defer func() {
		returned = errors.Join(returned, p.closeLocked())
		if returned != nil {
			p.ioFailure = errors.Join(p.ioFailure, returned)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.receiver.FinishContext(ctx); err != nil {
		return fmt.Errorf("verify runtime part: %w", err)
	}
	if err := p.file.Chmod(Mode(p.item)); err != nil {
		return fmt.Errorf("set runtime file mode: %w", err)
	}
	if err := stampArrival(p.file, time.Now()); err != nil {
		return fmt.Errorf("record runtime arrival: %w", err)
	}
	if err := p.file.Sync(); err != nil {
		return fmt.Errorf("sync runtime part: %w", err)
	}
	// Hold a checked read-only alias of THIS inode before retiring its sole
	// writer. A failed writer Close can never link a publishable artifact.
	reader, err := openNoFollow(p.incoming, p.basename, os.O_RDONLY, Mode(p.item))
	if err != nil {
		return err
	}
	p.reader = reader
	own, err := p.file.Stat()
	if err != nil {
		return err
	}
	actual, err := reader.Stat()
	if err != nil || !os.SameFile(own, actual) {
		return errors.Join(err, fmt.Errorf("runtime part changed before writer retirement"))
	}
	if err := p.closeWriterV1(); err != nil {
		return err
	}
	return retryExecMode(ctx, func() error {
		return p.store.withPublication(ctx, func() error {
			return p.store.locked(ctx, func() error {
				has, err := p.store.hasContext(ctx, p.item)
				if err != nil {
					return err
				}
				if has {
					return p.store.installVerityLocked(ctx, p.item)
				}
				if err := p.store.removeCorruptContext(ctx, p.item); err != nil {
					return err
				}
				if p.ioFailure != nil || !p.writerReleased {
					return fmt.Errorf("the original runtime writer has no successful durable retirement")
				}
				before, err := p.incoming.Lstat(p.basename)
				if err != nil || !os.SameFile(before, actual) {
					return errors.Join(err, fmt.Errorf("runtime part changed before publication"))
				}
				target := "sha256/" + p.item.SHA256
				if err := linkRuntimePartV1(p, target); err != nil {
					if errors.Is(err, os.ErrExist) {
						valid, err := p.store.hasContext(ctx, p.item)
						if err != nil {
							return err
						}
						if !valid {
							return fmt.Errorf("runtime publication winner is corrupt")
						}
						return p.store.installVerityLocked(ctx, p.item)
					}
					return fmt.Errorf("publish runtime file: %w", err)
				}
				if p.store.afterLink != nil {
					p.store.afterLink()
				}
				if err := p.removeOriginalNameV1(); err != nil {
					return fmt.Errorf("unlink own published runtime part: %w", err)
				}
				p.removed = true
				if err := p.syncOriginalParentV1(); err != nil {
					return err
				}
				p.incomingSynced = true
				installed, err := p.store.root.Lstat(target)
				if err != nil || !os.SameFile(installed, actual) {
					return errors.Join(err, fmt.Errorf("runtime publication changed inode"))
				}
				if err := p.store.installVerityLocked(ctx, p.item); err != nil {
					return err
				}
				return p.syncPublishedDirectoryV1()
			})
		})
	})
}

func (p *Part) closeWriterV1() error {
	if p.writerReleased {
		return nil
	}
	if p.writerAttempted {
		return p.writerClose.Unknown
	}
	p.writerAttempted = true
	p.writerClose = supply.CloseFileOnceV1(p.file)
	p.writerReleased = p.writerClose.Retired
	err := errors.Join(p.writerClose.Diagnostic, p.writerClose.Unknown)
	if err != nil {
		p.ioFailure = errors.Join(p.ioFailure, err)
	}
	return err
}
func (p *Part) closeReaderV1() error {
	if p.reader == nil || p.readerReleased {
		return nil
	}
	if p.readerAttempted {
		return p.readerClose.Unknown
	}
	p.readerAttempted = true
	p.readerClose = supply.CloseFileOnceV1(p.reader)
	p.readerReleased = p.readerClose.Retired
	return errors.Join(p.readerClose.Diagnostic, p.readerClose.Unknown)
}
func (p *Part) originalFileV1() *os.File {
	if p.reader != nil && !p.readerReleased {
		return p.reader
	}
	return p.file
}

// Close deletes this transfer's part when the name still refers to its file.
// A replaced or foreign file is never removed during cleanup.
func (p *Part) Close() error { p.mu.Lock(); defer p.mu.Unlock(); return p.closeLocked() }
func (p *Part) closeLocked() error {
	if p.closed {
		return nil
	}
	p.closing = true // Reject new writes while the SAME cleanup remains retryable.
	if !p.removed {
		st, err := p.incoming.Lstat(p.basename)
		own, ferr := p.originalFileV1().Stat()
		if err != nil || ferr != nil {
			return errors.Join(err, ferr)
		}
		if !st.Mode().IsRegular() || !os.SameFile(st, own) {
			return fmt.Errorf("the original runtime part changed identity during cleanup")
		}
		if err := p.removeOriginalNameV1(); err != nil {
			return fmt.Errorf("remove own runtime part: %w", err)
		}
		p.removed = true
	}
	if !p.incomingSynced {
		if err := p.syncOriginalParentV1(); err != nil {
			return err
		}
		p.incomingSynced = true
	}
	if err := p.closeWriterV1(); err != nil {
		return err
	}
	if err := p.closeReaderV1(); err != nil {
		return err
	}
	if err := p.syncPublishedDirectoryV1(); err != nil {
		return err
	}
	if err := p.closePublicationDirectoryV1(); err != nil {
		return err
	}
	if !p.parentReleased {
		err := p.incoming.Close()
		if err != nil {
			return fmt.Errorf("close original runtime incoming directory: %w", err)
		}
		p.parentReleased = true
	}
	p.closed = true
	return nil
}

func (p *Part) removeOriginalNameV1() error {
	own, err := p.originalFileV1().Stat()
	current, inspect := p.incoming.Lstat(p.basename)
	if err != nil || inspect != nil {
		return errors.Join(err, inspect)
	}
	if !current.Mode().IsRegular() || !os.SameFile(own, current) {
		return fmt.Errorf("the original runtime part name changed identity")
	}
	return p.incoming.Remove(p.basename)
}

func (p *Part) syncOriginalParentV1() error {
	if p.incomingSync == nil {
		actual, err := p.incoming.Open(".")
		if err != nil {
			return fmt.Errorf("open original runtime incoming directory for sync: %w", err)
		}
		p.incomingSync = actual
	}
	if !p.incomingDataSynced {
		if err := syncDirectory(p.incomingSync); err != nil {
			return fmt.Errorf("sync original runtime incoming directory: %w", err)
		}
		p.incomingDataSynced = true
	}
	if !p.incomingSyncCloseAttempted {
		p.incomingSyncCloseAttempted = true
		p.incomingSyncClose = supply.CloseFileOnceV1(p.incomingSync)
		return errors.Join(p.incomingSyncClose.Diagnostic, p.incomingSyncClose.Unknown)
	}
	return p.incomingSyncClose.Unknown
}

func (p *Part) closePublicationDirectoryV1() error {
	if p.publicationDir == nil || p.publicationDirReleased {
		return nil
	}
	if p.publicationCloseAttempted {
		return p.publicationClose.Unknown
	}
	p.publicationCloseAttempted = true
	p.publicationClose = supply.CloseFileOnceV1(p.publicationDir)
	p.publicationDirReleased = p.publicationClose.Retired
	return errors.Join(p.publicationClose.Diagnostic, p.publicationClose.Unknown)
}

// Retain the actual destination directory until its successful durable sync.
// A later same-name directory cannot substitute for this publication parent.
func (p *Part) syncPublishedDirectoryV1() error {
	if !p.linked || p.publicationSynced {
		return nil
	}
	if p.publicationDir == nil {
		return fmt.Errorf("runtime publication lost its original directory")
	}
	if p.store.beforePartSync != nil {
		if err := p.store.beforePartSync(p.publicationDir); err != nil {
			return err
		}
	}
	if err := syncDirectory(p.publicationDir); err != nil {
		return fmt.Errorf("sync original runtime publication directory: %w", err)
	}
	p.publicationSynced = true
	return nil
}
