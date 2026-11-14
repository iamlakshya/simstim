package nativememory

import (
	"context"
	"errors"
	"os"
	"sync"
)

// Current owns actual current-process accounting descriptors. No exported
// provider or constructor accepts guest paths, PIDs or made-up limits.
type Current struct {
	mu       sync.RWMutex
	owner    context.Context
	backend  currentBackend
	closed   bool
	closeErr error
}

type currentBackend interface {
	observe(context.Context) (sampleData, error)
	check(context.Context) error
	match(context.Context, *os.File, bool) error
	close() error
}

type sampleData struct {
	physical uint64
	ancestor Limit
}

// Sample is an opaque reading from one retained native scope. It is a
// time-varying observation, not an atomic allocation reservation or grant.
type Sample struct {
	owner    *Current
	observed context.Context
	data     sampleData
	err      error
}

func (c *Current) checkOpen(ctx context.Context) error {
	if ctx == nil || c == nil || c.backend == nil || c.owner == nil || c.closed {
		return errors.Join(ErrUnknown, os.ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.owner.Err()
}

// Observe reads physical availability and every visible ancestor constraint
// under this native descriptor owner. Unknown facts return an error.
func (c *Current) Observe(ctx context.Context) (Sample, error) {
	if c == nil {
		return Sample{}, ErrUnknown
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.checkOpen(ctx); err != nil {
		return Sample{}, err
	}
	bound, stop := c.readContext(ctx)
	defer stop()
	data, err := c.backend.observe(bound)
	err = errors.Join(err, c.checkOpen(ctx), bound.Err())
	return Sample{owner: c, observed: ctx, data: data, err: err}, err
}

// CheckCurrent repeats current-process membership and native descriptor checks.
func (c *Current) CheckCurrent(ctx context.Context) error {
	if c == nil {
		return ErrUnknown
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.checkOpen(ctx); err != nil {
		return err
	}
	bound, stop := c.readContext(ctx)
	defer stop()
	return errors.Join(c.backend.check(bound), c.checkOpen(ctx), bound.Err())
}

// MatchAncestor compares an actual owned directory FD with the captured
// current hierarchy. It accepts no pathname or numeric descriptor claim.
func (c *Current) MatchAncestor(ctx context.Context, directory *os.File) error {
	return c.match(ctx, directory, false)
}

// MatchHierarchyRoot compares the actual pinned visible hierarchy root FD.
// Its semantic completeness still needs the caller's real VM/unit receipt.
func (c *Current) MatchHierarchyRoot(ctx context.Context, directory *os.File) error {
	return c.match(ctx, directory, true)
}

func (c *Current) match(ctx context.Context, directory *os.File, rootOnly bool) error {
	if c == nil || directory == nil {
		return ErrUnknown
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.checkOpen(ctx); err != nil {
		return err
	}
	bound, stop := c.readContext(ctx)
	defer stop()
	return errors.Join(c.backend.match(bound, directory, rootOnly), c.checkOpen(ctx), bound.Err())
}

func (c *Current) readContext(ctx context.Context) (context.Context, func()) {
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.owner, cancel)
	return bound, func() { stop(); cancel() }
}

// Close joins owned reads before closing each accounting descriptor. It is
// idempotent and preserves the original close error on every later call.
func (c *Current) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	if c.backend != nil {
		c.closeErr = c.backend.close()
		c.backend = nil
	}
	return c.closeErr
}

func (s Sample) check() error {
	if s.err != nil {
		return s.err
	}
	if s.owner == nil {
		return ErrUnknown
	}
	if s.observed == nil {
		return ErrUnknown
	}
	if err := s.observed.Err(); err != nil {
		return err
	}
	s.owner.mu.RLock()
	defer s.owner.mu.RUnlock()
	return s.owner.checkOpen(context.Background())
}

// PhysicalAvailable returns the actual kernel MemAvailable reading.
func (s Sample) PhysicalAvailable() (uint64, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	return s.data.physical, nil
}

// VisibleAncestorHeadroom returns the complete known visible-chain bound.
// A namespace-clipped root is not proof that hidden ancestors are unlimited.
func (s Sample) VisibleAncestorHeadroom() (Limit, error) {
	if err := s.check(); err != nil {
		return Limit{}, err
	}
	if s.data.ancestor.kind == Unknown {
		return Limit{}, ErrUnknown
	}
	return s.data.ancestor, nil
}

// VisibleAdditionalHeadroom takes the minimum physical/ancestor reading.
// The real owner must bind hierarchy completeness before using it to admit
// additional allocation, and must still handle actual allocation failures.
func (s Sample) VisibleAdditionalHeadroom() (uint64, error) {
	limit, err := s.VisibleAncestorHeadroom()
	if err != nil {
		return 0, err
	}
	if limit.kind == Unlimited {
		return s.data.physical, nil
	}
	return min(s.data.physical, limit.bytes), nil
}

func remaining(limit Limit, current uint64) (Limit, error) {
	if limit.kind == Unlimited {
		return limit, nil
	}
	if limit.kind != Finite {
		return Limit{}, ErrUnknown
	}
	if current >= limit.bytes {
		return Limit{kind: Finite}, nil
	}
	return Limit{kind: Finite, bytes: limit.bytes - current}, nil
}

func tighter(a, b Limit) (Limit, error) {
	if a.kind == Unknown || b.kind == Unknown {
		return Limit{}, ErrUnknown
	}
	if a.kind == Unlimited {
		return b, nil
	}
	if b.kind == Unlimited {
		return a, nil
	}
	return Limit{kind: Finite, bytes: min(a.bytes, b.bytes)}, nil
}
