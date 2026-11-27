package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

type physicalH2RequestKey struct{}

// Connection-bound delivery correlation, never a permission or Source table.
// Active server concurrency remains128. Retiring reset streams have one
// additional flow-sized bookkeeping envelope until their ordered writer fence.
type physicalH2Owner struct {
	mu            sync.Mutex
	key           *physicalH2TokenKey
	streams       map[uint32]*physicalH2Delivery
	changed       chan struct{}
	closed        bool
	lastClient    uint32
	fences        map[[8]byte]uint32
	guestPings    map[[8]byte]uint32
	guestPingN    int
	writerWaiters int
	handlers      sync.WaitGroup
	callbacks     sync.WaitGroup
	tlsState      tls.ConnectionState
	scheduler     bool
	injectReset   func(uint32)
	writeReset    func(uint32) error
}

type physicalH2Delivery struct {
	owner          *physicalH2Owner
	id             uint32
	ctx            context.Context
	cancel         context.CancelFunc
	claimed        bool
	truncated      bool
	returned       bool
	complete       bool
	settled        bool
	reset          bool
	cancelled      bool
	serverClosed   bool
	selected       uint64
	resetRequested bool
	nativeReset    bool
	finished       bool
	turn           *ModelUsageTurnV1
	finish         func(ModelUsageCompletionV1)
	beforeFinish   func()
	observing      bool
}

func (*physicalH2Owner) Format(f fmt.State, _ rune)    { _, _ = io.WriteString(f, api.Redacted) }
func (*physicalH2Delivery) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newPhysicalH2Owner(state tls.ConnectionState) (*physicalH2Owner, error) {
	key, err := newPhysicalH2TokenKey()
	if err != nil {
		return nil, err
	}
	return &physicalH2Owner{key: key, streams: make(map[uint32]*physicalH2Delivery), changed: make(chan struct{}), fences: make(map[[8]byte]uint32), guestPings: make(map[[8]byte]uint32), tlsState: state}, nil
}

func (o *physicalH2Owner) notifyLocked() {
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *physicalH2Owner) introduce(ctx context.Context, id uint32) (*physicalH2Delivery, bool, error) {
	for {
		o.mu.Lock()
		if o.closed || id == 0 || id&1 == 0 {
			o.mu.Unlock()
			return nil, false, errPhysicalH2Binding
		}
		if id <= o.lastClient {
			stream := o.streams[id]
			o.mu.Unlock()
			return stream, false, nil // trailers or an old stream; never a new binding
		}
		if len(o.streams) < 2*maxActiveFlows {
			stream := &physicalH2Delivery{owner: o, id: id}
			o.streams[id], o.lastClient = stream, id
			o.mu.Unlock()
			return stream, true, nil
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-changed:
		}
	}
}

// Called before the original handler, canonicalisation, policy or origin IO.
// The standard server receives no TLS-state interface on its plaintext pipe;
// the actual handshake state is restored here after private token removal.
func (o *physicalH2Owner) claim(request *http.Request) (*http.Request, *physicalH2Delivery, error) {
	id, err := o.key.consume(request)
	if err != nil {
		return request, nil, err
	}
	o.mu.Lock()
	stream := o.streams[id]
	if o.closed || stream == nil || stream.claimed || stream.reset || stream.finished {
		o.mu.Unlock()
		return request, nil, errPhysicalH2Binding
	}
	stream.claimed = true
	// Handler return cancels net/http's own context. Delivery continues until
	// the transport fence; original deadlines, reset and connection cancellation
	// remain explicit, as does the original scope's own policy cancellation.
	base := context.WithoutCancel(request.Context())
	var ctx context.Context
	if deadline, ok := request.Context().Deadline(); ok {
		ctx, stream.cancel = context.WithDeadline(base, deadline)
	} else {
		ctx, stream.cancel = context.WithCancel(base)
	}
	stream.ctx = ctx
	o.mu.Unlock()
	state := o.tlsState
	request.TLS = &state
	request = request.WithContext(context.WithValue(ctx, physicalH2RequestKey{}, stream))
	return request, stream, nil
}

func physicalH2RequestDelivery(request *http.Request) *physicalH2Delivery {
	if request == nil {
		return nil
	}
	stream, _ := request.Context().Value(physicalH2RequestKey{}).(*physicalH2Delivery)
	return stream
}

// Retain the original turn and original owner's completion callback. This
// cannot begin a model turn, look up a value or acquire a network permit.
func (s *physicalH2Delivery) retain(turn *ModelUsageTurnV1, finish func(ModelUsageCompletionV1)) error {
	o := s.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !s.claimed || s.finished || s.turn != nil || turn == nil {
		return errPhysicalH2Binding
	}
	s.turn, s.finish, s.observing = turn, finish, true
	return nil
}

func (s *physicalH2Delivery) handlerFinished(complete bool) {
	o := s.owner
	o.mu.Lock()
	s.returned, s.complete = true, complete
	finish := o.finishLocked(s)
	o.mu.Unlock()
	if finish != nil {
		go finish()
	}
}

// The caller observed successful plaintext prefixes before setting settled.
// Cancellation alone never calls this method or establishes success.
func (o *physicalH2Owner) finishLocked(s *physicalH2Delivery) func() {
	if s.finished || s.claimed && !s.returned {
		return nil
	}
	if o.scheduler && s.selected != 0 {
		// END_STREAM can be followed by already selected stream controls.
		// Every selection retains its owner until the native frame finishes
		// or the original connection is cut and its writers are joined.
		return nil
	}
	if !s.settled && !(o.scheduler && s.serverClosed && s.reset && s.selected == 0) {
		return nil
	}
	s.finished = true
	// Keep the same bounded row charged until its original completion job
	// has run. A slow audit or Source cleanup must not block the writer or
	// permit an unbounded population of detached callbacks.
	o.callbacks.Add(1)
	nativeReset, writeReset := o.scheduler && s.nativeReset && !s.settled, o.writeReset
	complete, turn, callback, cancel, before := s.complete && !s.reset && !s.cancelled, s.turn, s.finish, s.cancel, s.beforeFinish
	s.turn, s.finish, s.beforeFinish = nil, nil, nil
	return func() {
		defer o.callbacks.Done()
		defer func() {
			o.mu.Lock()
			if o.streams[s.id] == s {
				delete(o.streams, s.id)
				o.notifyLocked()
			}
			o.mu.Unlock()
		}()
		if cancel != nil {
			defer cancel()
		}
		if nativeReset && writeReset != nil {
			if err := writeReset(s.id); err != nil {
				complete = false
			}
		}
		if before != nil {
			before()
		}
		var completion ModelUsageCompletionV1
		if turn != nil {
			completion = turn.FinishV1(complete)
		}
		if callback != nil {
			callback(completion)
		}
	}
}

func (o *physicalH2Owner) physicalData(id uint32, payload []byte) error {
	o.mu.Lock()
	stream := o.streams[id]
	if stream == nil || stream.finished {
		o.mu.Unlock()
		return errPhysicalH2Binding
	}
	turn := stream.turn
	if !stream.observing {
		turn = nil
	}
	o.mu.Unlock()
	if turn != nil {
		turn.ForwardedV1(payload)
	}
	return nil
}

// The original handler installs its sole finaliser before returning. The
// transport's ordered writer fence selects completion, then joins origin IO
// before settling the original meter and releasing Source/permit/worker state.
func (s *physicalH2Delivery) finalizer(complete bool, before func(), finish func(ModelUsageCompletionV1)) {
	s.owner.mu.Lock()
	s.complete, s.beforeFinish, s.finish = complete, before, finish
	s.owner.mu.Unlock()
}

func (s *physicalH2Delivery) invalidate() {
	o := s.owner
	o.mu.Lock()
	s.cancelled = true
	inject, cancel := o.injectReset, s.cancel
	queued := o.scheduler && !o.closed && !s.finished && !s.resetRequested
	if queued {
		s.resetRequested, s.reset, s.nativeReset = true, true, true
	}
	finish := o.finishLocked(s)
	o.mu.Unlock()
	if finish != nil {
		go finish()
	}
	if cancel != nil {
		cancel()
	}
	if queued && inject != nil {
		inject(s.id)
	}
}

func (s *physicalH2Delivery) observation(active bool) {
	s.owner.mu.Lock()
	s.observing = active
	s.owner.mu.Unlock()
}
