package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SessionOperation retains the canonical primary op.lock across a complete
// operation/retry. Only its owner releases it, after owned resource cleanup.
type SessionOperation struct {
	automaticBoot *AutomaticBootOperationV1
	id            string
	native        *sessionFileLease
	ownerContext  context.Context
	lifetime      context.Context
	cancel        context.CancelFunc
	gate          sync.Mutex
	readers       sync.WaitGroup
	active        atomic.Bool
	once          sync.Once
	closeErr      error
}

// BeginSessionOperation resolves and locks one protected primary. It never
// creates a second lease file or releases on cancellation before cleanup.
func BeginSessionOperation(ctx context.Context, original string, wait time.Duration) (*SessionOperation, error) {
	primary, err := SandboxPrimaryID(original)
	if err != nil {
		return nil, err
	}
	native, err := openOwnedSessionFileLease(ctx, primary, lockFile, wait)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = native.Close()
		return nil, err
	}
	actual, err := SandboxPrimaryID(original)
	if err != nil || actual != primary {
		_ = native.Close()
		if err != nil {
			return nil, err
		}
		return nil, snapshotChanged()
	}
	return sessionOperationFromNative(ctx, primary, native), nil
}

func sessionOperationFromNative(ctx context.Context, primary string, native *sessionFileLease) *SessionOperation {
	lifetime, cancel := context.WithCancel(context.Background())
	operation := &SessionOperation{id: primary, native: native, ownerContext: ctx, lifetime: lifetime, cancel: cancel}
	operation.active.Store(true)
	return operation
}

// beginReservedSessionOperation belongs only to the actual New reservation,
// before its first canonical save. It cannot adopt an arbitrary same-name dir.
func beginReservedSessionOperation(ctx context.Context, reservation *sessionReservation) (*SessionOperation, error) {
	if reservation == nil || reservation.parent == nil || reservation.dir == nil || reservation.created == nil || !validID(reservation.id) {
		return nil, memberError("new session operation has no original reservation")
	}
	current, err := reservation.parent.Lstat(reservation.id)
	if err != nil || !os.SameFile(reservation.created, current) {
		return nil, memberError("the original reserved session directory changed")
	}
	native, err := openOwnedSessionFileLease(ctx, reservation.id, lockFile, 0)
	if err != nil {
		return nil, err
	}
	if err := native.rootRef.CheckRoot(reservation.dir); err != nil {
		return nil, errors.Join(err, native.Close())
	}
	current, err = reservation.parent.Lstat(reservation.id)
	if err != nil || !os.SameFile(reservation.created, current) {
		return nil, errors.Join(memberError("the reserved session directory changed during OP admission"), native.Close())
	}
	return sessionOperationFromNative(ctx, reservation.id, native), nil
}

// Primary is the stable protected primary, never host-supplied local authority.
func (o *SessionOperation) Primary() string {
	if o == nil {
		return ""
	}
	return o.id
}

// Valid reports whether this same operation still owns the primary lease.
func (o *SessionOperation) Valid(primary string) bool {
	return o != nil && o.id == primary && o.active.Load() && o.native != nil &&
		o.native.check(context.Background()) == nil && o.active.Load()
}

// Load reads fresh protected metadata without reacquiring its retained lease.
func (o *SessionOperation) Load() (*Manifest, error) {
	if o == nil || o.ownerContext == nil {
		return nil, memberError("the primary operation has no actual owner lifetime")
	}
	return o.LoadContext(o.ownerContext)
}

// LoadContext uses the one canonical Manifest decoder on a checked original
// descriptor. Concurrent close cancels and joins this reader before native
// OP closure; no later path or same-name record can renew its source.
func (o *SessionOperation) LoadContext(ctx context.Context) (*Manifest, error) {
	var result *Manifest
	failure := o.withCurrentRead(ctx, func(owned context.Context) (failure error) {
		file, err := cachefs.OpenFile(o.native.root, "manifest.json", os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		defer func() { failure = errors.Join(failure, file.Close()) }()
		reference, err := cachefs.CaptureFile(o.native.root, "manifest.json", file, 0o600)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: owned, reader: file}, int64(proto.MaxFrame)+1))
		if err != nil {
			return err
		}
		if len(raw) > proto.MaxFrame {
			return memberError("the protected primary manifest exceeds its wire bound")
		}
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return fmt.Errorf("decode the protected primary manifest: %w", err)
		}
		if manifest.ID != o.id || manifest.SandboxOf != "" || manifest.path != "" {
			return memberError("the operation no longer names its original protected primary")
		}
		if err := reference.CheckFile(o.native.root, "manifest.json", file, 0o600); err != nil {
			return err
		}
		if err := o.native.check(owned); err != nil {
			return err
		}
		result = &manifest
		return nil
	})
	if failure != nil {
		return nil, failure
	}
	return result, nil
}

func (o *SessionOperation) withCurrentRead(ctx context.Context, read func(context.Context) error) error {
	if o == nil || ctx == nil || read == nil {
		return memberError("the primary operation has no owned reader")
	}
	o.gate.Lock()
	if !o.active.Load() || o.native == nil || o.lifetime == nil {
		o.gate.Unlock()
		return memberError("the primary operation ended")
	}
	o.readers.Add(1)
	o.gate.Unlock()
	defer o.readers.Done()
	owned, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	stop := context.AfterFunc(o.lifetime, func() { defer close(done); cancel() })
	defer func() {
		if !stop() {
			<-done
		}
		cancel()
	}()
	if err := o.native.check(owned); err != nil {
		return err
	}
	if err := read(owned); err != nil {
		return err
	}
	return o.native.check(owned)
}

type sessionOperationReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader sessionOperationReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(data)
	if canceled := reader.ctx.Err(); canceled != nil {
		return n, canceled
	}
	return n, err
}

// Close cancels and joins actual readers before closing this descriptor.
// Kernel OFD lifetime keeps a live borrow locked; no LOCK_UN is issued here.
func (o *SessionOperation) Close() { _ = o.CloseWithError() }

// CloseWithError preserves native closure errors without claiming completion.
func (o *SessionOperation) CloseWithError() error {
	if o == nil {
		return nil
	}
	o.once.Do(func() {
		o.gate.Lock()
		o.active.Store(false)
		if o.cancel != nil {
			o.cancel()
		}
		o.gate.Unlock()
		o.readers.Wait()
		if o.native == nil {
			o.closeErr = memberError("the operation has no native descriptor owner")
		} else {
			o.closeErr = o.native.Close()
		}
	})
	return o.closeErr
}

// StoppedGuest is an operation-owned control link, with no ordinary session.
type StoppedGuest interface {
	Manifest() Manifest
	Call(context.Context, string, any, any) error
	Open(context.Context, link.Kind, any) (*link.Stream, error)
	Close() error
}

// StoppedGuestOpener is installed once by cmd/host, avoiding a host/supervisor
// import cycle. It receives the already retained primary lease.
type StoppedGuestOpener func(context.Context, *SessionOperation) (StoppedGuest, error)

var stoppedGuestOpener StoppedGuestOpener

// SetStoppedGuestOpener registers the real resource consumer, or a test fixture.
func SetStoppedGuestOpener(open StoppedGuestOpener) func() {
	old := stoppedGuestOpener
	stoppedGuestOpener = open
	return func() { stoppedGuestOpener = old }
}

// OpenStoppedGuest calls the registered real backend under the same operation.
func OpenStoppedGuest(ctx context.Context, operation *SessionOperation) (StoppedGuest, error) {
	if operation == nil || !operation.Valid(operation.Primary()) {
		return nil, memberError("stopped export has no primary lease")
	}
	if stoppedGuestOpener == nil {
		return nil, UserError("helper_old", "the host command has no resource consumer", "Use the current host helper entrypoint.")
	}
	return stoppedGuestOpener(ctx, operation)
}
