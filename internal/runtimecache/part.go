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

	"github.com/iamlakshya/simstim/internal/supply"
)

// Part owns one exclusively created partial file. No other transfer may
// write it, publish it or remove it. A failed write closes only this part.
type Part struct {
	mu       sync.Mutex
	store    *Store
	file     *os.File
	item     supply.Item
	name     string
	receiver *supply.Receiver
	started  time.Time
	closed   bool
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
	name := "incoming/" + item.SHA256 + "." + hex.EncodeToString(nonce[:]) + ".part"
	f, err := openNoFollow(s.root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create runtime part: %w", err)
	}
	part := &Part{store: s, file: f, item: item, name: name, started: time.Now()}
	st, err := f.Stat()
	if err != nil || !descriptorSafe(f, st) || !modeMatches(st, 0o600) {
		part.Close()
		return nil, fmt.Errorf("new runtime part does not have private ownership")
	}
	receiver, err := supply.NewReceiver(item, f)
	if err != nil {
		part.Close()
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
	return p.receiver.Received(), p.started
}

// WriteAt appends one bounded chunk at the exact next offset. A replay or
// over-size chunk deletes this part before any of that chunk is written.
func (p *Part) WriteAt(data []byte, offset int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("runtime transfer is closed")
	}
	if err := p.receiver.WriteAt(data, offset); err != nil {
		_ = p.closeLocked()
		return fmt.Errorf("receive runtime chunk: %w", err)
	}
	return nil
}

// Finish verifies this part, then atomically publishes it without replacing
// a checked existing winner. It removes only its own incoming file.
func (p *Part) Finish(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("runtime transfer is closed")
	}
	defer p.closeLocked() // A mismatch or cancelled publication keeps no own part.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.receiver.FinishContext(ctx); err != nil {
		return fmt.Errorf("verify runtime part: %w", err)
	}
	if err := p.file.Chmod(Mode(p.item)); err != nil {
		return fmt.Errorf("set runtime file mode: %w", err)
	}
	return p.store.withPublication(ctx, func() error {
		return p.store.locked(ctx, func() error {
			has, err := p.store.hasContext(ctx, p.item)
			if err != nil {
				return err
			}
			if has {
				return nil
			}
			if err := stampArrival(p.file, time.Now()); err != nil {
				return fmt.Errorf("record runtime arrival: %w", err)
			}
			if err := p.file.Sync(); err != nil {
				return fmt.Errorf("sync runtime part: %w", err)
			}
			if err := p.store.removeCorruptContext(ctx, p.item); err != nil {
				return err
			}
			before, err := p.store.root.Lstat(p.name)
			own, ferr := p.file.Stat()
			if err != nil || ferr != nil || !os.SameFile(before, own) {
				return fmt.Errorf("runtime part changed before publication")
			}
			target := "sha256/" + p.item.SHA256
			if err := p.store.root.Link(p.name, target); err != nil {
				if errors.Is(err, os.ErrExist) {
					var valid bool
					valid, err = p.store.hasContext(ctx, p.item)
					if err != nil {
						return err
					}
					if !valid {
						return fmt.Errorf("runtime publication winner is corrupt")
					}
					return nil
				}
				return fmt.Errorf("publish runtime file: %w", err)
			}
			if p.store.afterLink != nil {
				p.store.afterLink()
			}
			if err := p.store.root.Remove(p.name); err != nil {
				return fmt.Errorf("unlink own published runtime part: %w", err)
			}
			if err := p.store.syncDir("incoming"); err != nil {
				return err
			}
			installed, err := p.store.root.Lstat(target)
			if err != nil || !os.SameFile(installed, own) {
				return fmt.Errorf("runtime publication changed inode")
			}
			return p.store.syncDir("sha256")
		})
	})
}

// Close deletes this transfer's part when the name still refers to its file.
// A replaced or foreign file is never removed during cleanup.
func (p *Part) Close() error { p.mu.Lock(); defer p.mu.Unlock(); return p.closeLocked() }
func (p *Part) closeLocked() error {
	if p.closed {
		return nil
	}
	p.closed = true
	st, err := p.store.root.Lstat(p.name)
	own, ferr := p.file.Stat()
	var removeErr error
	if err == nil && ferr == nil && os.SameFile(st, own) {
		removeErr = p.store.root.Remove(p.name)
	}
	closeErr := p.file.Close()
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("remove own runtime part: %w", removeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close runtime part: %w", closeErr)
	}
	return nil
}
