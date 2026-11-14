// Package pullexport owns bounded, connection-scoped immutable bundle spools.
// Actual process, backend and primary operation leases stay with Source.
package pullexport

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CaptureArgs fixes one history mode and the first sealed branch snapshot.
type CaptureArgs struct {
	Full       bool
	Heads      []proto.BundleRef
	DefaultRef string
	// Progress renews inactivity only while the exact owned capture is alive.
	Progress func()
}

// CaptureResult is untrusted guest metadata, checked against sealed bytes.
type CaptureResult struct {
	Empty      bool
	Full       bool
	DefaultRef string
}

// Source owns exactly one real primary lease/backend/control connection.
// Capture reuses it for a full retry. Close must cancel and join its own
// processes without destroying workspace or publishing session/file state.
type Source interface {
	// PullMax is validated from fresh protected policy under the same lease.
	PullMax() int64
	SeedHeads() []string
	Capture(context.Context, CaptureArgs, io.Writer) (CaptureResult, error)
	RecordPulled(context.Context, []string) error
	Close() error
}

// Open claims an actual canonical Source, never a different process engine.
type Open func(ctx context.Context, originalID, expectedPrimary string) (Source, error)

// Options contains only owned spool and inactivity choices. Clock/ticks are
// deterministic test seams. No whole-operation deadline shortens live work.
type Options struct {
	Directory func(string) (string, error)
	Open      Open
	Idle      time.Duration
	Now       func() time.Time
	Ticks     <-chan time.Time
	StopTicks func()
}

// Manager limits one export per primary and four per outer owner.
type Manager struct {
	mu      sync.Mutex
	life    context.Context
	options Options
	exports map[string]*export
	primary map[string]*export
	ticker  *time.Ticker
	done    chan struct{}
	cancel  context.CancelFunc
}

// New constructs the domain owner. The caller must Close it on disconnect.
func New(life context.Context, options Options) (*Manager, error) {
	if life == nil || options.Directory == nil || options.Open == nil {
		return nil, fmt.Errorf("pull export owner is incomplete")
	}
	if options.Idle <= 0 {
		options.Idle = 2 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	owned, cancel := context.WithCancel(life)
	manager := &Manager{life: owned, cancel: cancel, options: options, exports: map[string]*export{}, primary: map[string]*export{}, done: make(chan struct{})}
	if options.Ticks == nil {
		interval := max(time.Nanosecond, min(options.Idle/4, 30*time.Second))
		manager.ticker = time.NewTicker(interval)
		manager.options.Ticks = manager.ticker.C
		manager.options.StopTicks = manager.ticker.Stop
	}
	go manager.watch()
	return manager, nil
}

func exportError(code, message string) error {
	return &proto.Error{Code: code, Message: message, Hint: "Close the current pull or retry after checking the sandbox."}
}

// Begin registers caller/primary/token ownership before any potentially slow
// source action. A full retry transfers only its own existing resource handle.
func (m *Manager) Begin(ctx context.Context, owner, primary string, args api.PullBeginArgs, limit int64) (api.PullExport, error) {
	run, err := m.PrepareBegin(ctx, owner, primary, args, limit)
	if err != nil {
		return api.PullExport{}, err
	}
	return run(ctx)
}

// PrepareBegin admits a nonce synchronously on the normal RPC reader. The
// returned capture may run asynchronously, so a following abort is already
// ordered after admission. Close can release a reservation that never ran.
func (m *Manager) PrepareBegin(ctx context.Context, owner, primary string, args api.PullBeginArgs, limit int64) (func(context.Context) (api.PullExport, error), error) {
	return m.PrepareBeginRequest(ctx, owner, primary, args, limit, 0)
}

// PrepareBeginRequest records the existing begin correlation ID as the work
// epoch. A following full request replaces it under the same retained source.
func (m *Manager) PrepareBeginRequest(ctx context.Context, owner, primary string, args api.PullBeginArgs, limit int64, epoch uint64) (func(context.Context) (api.PullExport, error), error) {
	if owner == "" || !api.ValidSessionID(primary) || !api.ValidSessionID(args.ID) || !api.ValidSessionID(args.Token) || len(args.Token) != 26 || limit <= 0 {
		return nil, exportError("pull_protocol", "invalid pull export admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := m.life.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	current := m.primary[primary]
	if current != nil {
		if m.exports[args.Token] != nil || current.owner != owner || current.id != args.ID || !args.Full || args.Previous != current.token || args.Token == current.token {
			m.mu.Unlock()
			return nil, exportError("sandbox_busy", "this sandbox already has an owned pull export")
		}
		current.mu.Lock()
		if current.closing || current.capturing || current.readBusy || !current.ready || current.full || current.offset != current.size {
			current.mu.Unlock()
			m.mu.Unlock()
			return nil, exportError("sandbox_busy", "the prior export is not ready for full retry")
		}
		delete(m.exports, current.token)
		current.token = args.Token
		current.epoch, current.readTicket, current.readBusy = epoch, nil, false
		current.full = true
		current.ready = false
		current.capturing = true
		current.workDone = make(chan struct{})
		workDone := current.workDone
		current.workStarted, current.workClosed = false, false
		current.last = m.options.Now()
		m.exports[args.Token] = current
		current.mu.Unlock()
		m.mu.Unlock()
		return func(ctx context.Context) (api.PullExport, error) { return current.capture(ctx, true, workDone) }, nil
	}
	if args.Previous != "" {
		m.mu.Unlock()
		return nil, exportError("pull_expired", "the prior pull export no longer owns its resource")
	}
	count := 0
	for _, active := range m.exports {
		if active.owner == owner {
			count++
		}
	}
	if count >= api.PullExportsMax || m.exports[args.Token] != nil {
		m.mu.Unlock()
		return nil, exportError("sandbox_busy", "all pull export slots for this connection are in use")
	}
	lifetime, cancel := context.WithCancel(m.life)
	current = &export{manager: m, owner: owner, primary: primary, id: args.ID, token: args.Token, epoch: epoch, full: args.Full, limit: limit, life: lifetime, cancel: cancel, closed: make(chan struct{}), workDone: make(chan struct{}), capturing: true, last: m.options.Now()}
	m.exports[args.Token] = current
	m.primary[primary] = current
	workDone := current.workDone
	m.mu.Unlock()
	return func(ctx context.Context) (api.PullExport, error) { return current.capture(ctx, args.Full, workDone) }, nil
}

func (m *Manager) find(owner, id, token string) (*export, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.exports[token]
	if current == nil || current.owner != owner || current.id != id {
		return nil, exportError("pull_protocol", "this connection does not own that pull export")
	}
	return current, nil
}

// Read returns sequential bytes or an exact EOF keepalive for this owner.
func (m *Manager) Read(ctx context.Context, owner string, args api.PullReadArgs) (api.PullChunk, error) {
	return m.ReadRequest(ctx, owner, args, 0)
}

// ReadRequest binds transport progress to the existing read correlation ID.
func (m *Manager) ReadRequest(ctx context.Context, owner string, args api.PullReadArgs, request uint64) (api.PullChunk, error) {
	run, err := m.PrepareReadRequest(ctx, owner, args, request)
	if err != nil {
		return api.PullChunk{}, err
	}
	return run(ctx)
}

// PrepareReadRequest fences the current request synchronously, before any
// async spool read. A delayed old transport event cannot renew that new read.
func (m *Manager) PrepareReadRequest(ctx context.Context, owner string, args api.PullReadArgs, request uint64) (func(context.Context) (api.PullChunk, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := m.find(owner, args.ID, args.Token)
	if err != nil {
		return nil, err
	}
	ticket, err := current.reserveRead(ctx, args.Offset, request)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (api.PullChunk, error) { return current.read(ctx, ticket) }, nil
}

// Touch renews real output progress only within the same existing read epoch.
// A stale, unknown, cancelled or completed scope has no resource effect.
func (m *Manager) Touch(ctx context.Context, owner string, progress api.PullOutputProgress) bool {
	if ctx.Err() != nil || m.life.Err() != nil || progress.Validate() != nil {
		return false
	}
	current, err := m.find(owner, progress.ID, progress.Token)
	if err != nil {
		return false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	ticket := current.readTicket
	if current.life.Err() != nil || current.closing || current.capturing || current.readBusy || !current.ready || ticket == nil || ticket.prepared.Err() != nil || ticket.run == nil || ticket.run.Err() != nil || current.epoch != progress.Epoch || ticket.epoch != progress.Epoch || ticket.request != progress.Request || current.offset != progress.Next || progress.Next > current.size {
		return false
	}
	current.last = m.options.Now()
	return true
}

// Finish records only sealed captured head IDs, then joins this export. Abort
// never records. Repeating an empty abort has no authority or resource effect.
func (m *Manager) Finish(ctx context.Context, owner string, args api.PulledArgs) (api.PulledResult, error) {
	current, err := m.find(owner, args.ID, args.Token)
	if err != nil {
		if len(args.Commits) == 0 {
			return api.PulledResult{}, nil
		}
		return api.PulledResult{}, err
	}
	defer current.close()
	if args.Abort {
		return api.PulledResult{}, nil
	}
	current.mu.Lock()
	if !current.ready || current.capturing || current.readBusy || current.closing || current.offset != current.size {
		current.mu.Unlock()
		return api.PulledResult{}, exportError("pull_protocol", "the pull export is not ready for acknowledgement")
	}
	allowed := map[string]bool{}
	for _, head := range current.heads {
		allowed[head.OID] = true
	}
	for _, oid := range args.Commits {
		if !api.ValidGitOID(oid) || !allowed[oid] {
			current.mu.Unlock()
			return api.PulledResult{}, exportError("pull_protocol", "acknowledged commit was not captured in this export")
		}
	}
	source := current.source
	current.last = m.options.Now()
	current.capturing = true
	current.workDone = make(chan struct{})
	current.workStarted, current.workClosed = true, false
	done := current.workDone
	current.mu.Unlock()
	defer func() { current.mu.Lock(); current.capturing = false; current.mu.Unlock(); close(done) }()
	operation, cancel := context.WithCancel(current.life)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	commits := append([]string(nil), args.Commits...)
	if err := ctx.Err(); err != nil {
		return api.PulledResult{}, err
	}
	if err := source.RecordPulled(operation, commits); err != nil {
		return api.PulledResult{}, err
	}
	return api.PulledResult{Commits: commits}, nil
}

func (m *Manager) watch() {
	defer close(m.done)
	if m.options.StopTicks != nil {
		defer m.options.StopTicks()
	}
	for {
		select {
		case <-m.life.Done():
			m.closeAll("")
			return
		case _, ok := <-m.options.Ticks:
			if !ok {
				m.closeAll("")
				return
			}
			m.mu.Lock()
			var stale []*export
			for _, current := range m.exports {
				current.mu.Lock()
				expired := m.options.Now().Sub(current.last) >= m.options.Idle
				current.mu.Unlock()
				if expired {
					stale = append(stale, current)
				}
			}
			m.mu.Unlock()
			for _, current := range stale {
				current.close()
			}
		}
	}
}

func (m *Manager) closeAll(owner string) {
	m.mu.Lock()
	var active []*export
	for _, current := range m.exports {
		if owner == "" || current.owner == owner {
			active = append(active, current)
		}
	}
	m.mu.Unlock()
	for _, current := range active {
		current.close()
	}
}

// CloseOwner joins every export of an outer connection on its disconnect.
func (m *Manager) CloseOwner(owner string) { m.closeAll(owner) }

// Close joins all owned exports. It does not destroy any sandbox workspace.
func (m *Manager) Close() { m.cancel(); <-m.done }
