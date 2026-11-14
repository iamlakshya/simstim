package pullexport

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type export struct {
	manager                   *Manager
	owner, primary, id, token string
	full                      bool
	epoch                     uint64
	readTicket                *readReservation
	readBusy                  bool
	limit                     int64
	life                      context.Context
	cancel                    context.CancelFunc
	mu                        sync.Mutex
	source                    Source
	file                      *os.File
	path                      string
	size, offset              int64
	heads                     []proto.BundleRef
	defaultRef                string
	seeds                     []string
	ready, capturing, closing bool
	last                      time.Time
	workDone                  chan struct{}
	workStarted, workClosed   bool
	closed                    chan struct{}
	once                      sync.Once
}

type cappedBundle struct {
	export *export
	file   *os.File
	count  int64
}

func (w *cappedBundle) Write(data []byte) (int, error) {
	if err := w.export.life.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.export.limit-w.count {
		return 0, exportError("pull_too_big", "the session bundle exceeds pull_max")
	}
	n, err := w.file.Write(data)
	w.count += int64(n)
	w.export.mu.Lock()
	w.export.last = w.export.manager.options.Now()
	w.export.mu.Unlock()
	return n, err
}

func (e *export) capture(caller context.Context, full bool, admitted <-chan struct{}) (_ api.PullExport, err error) {
	stop := context.AfterFunc(caller, e.cancel)
	defer stop()
	e.mu.Lock()
	if e.closing || e.workClosed || e.workStarted || e.workDone != admitted {
		e.mu.Unlock()
		return api.PullExport{}, exportError("pull_closed", "the admitted pull no longer owns a capture")
	}
	e.workStarted = true
	done := e.workDone
	e.mu.Unlock()
	finished := false
	finishWork := func() {
		if !finished {
			e.mu.Lock()
			e.capturing = false
			e.mu.Unlock()
			close(done)
			finished = true
		}
	}
	defer func() {
		finishWork()
		if err != nil {
			e.close()
		}
	}()
	if err = caller.Err(); err != nil {
		return api.PullExport{}, err
	}
	if err = e.life.Err(); err != nil {
		return api.PullExport{}, err
	}
	if e.source == nil {
		source, err := e.manager.options.Open(e.life, e.id, e.primary)
		if err != nil {
			return api.PullExport{}, err
		}
		if source == nil {
			return api.PullExport{}, exportError("pull_owner", "the canonical pull owner returned no resource")
		}
		e.mu.Lock()
		e.source = source
		e.seeds = append([]string(nil), source.SeedHeads()...)
		freshCap := source.PullMax()
		e.limit = min(e.limit, freshCap)
		e.mu.Unlock()
		if freshCap <= 0 {
			return api.PullExport{}, exportError("pull_policy", "the fresh protected bundle cap is invalid")
		}
		for _, seed := range e.seeds {
			if !api.ValidGitOID(seed) {
				return api.PullExport{}, exportError("pull_protocol", "protected seed history is invalid")
			}
		}
	}
	if err = e.life.Err(); err != nil {
		return api.PullExport{}, err
	}
	e.mu.Lock()
	snapshot := append([]proto.BundleRef(nil), e.heads...)
	defaultRef := e.defaultRef
	e.mu.Unlock()
	captured, err := e.captureSpool(caller, full, snapshot, defaultRef, done)
	if err != nil {
		return api.PullExport{}, err
	}
	if captured.result.Empty {
		descriptor := e.description(true)
		finishWork()
		e.close()
		return descriptor, nil
	}
	if !captured.full && foreignPrerequisites(captured.header.Prerequisites, e.seeds) {
		// Git may use an ancestor boundary when a branch diverged before a seed.
		// Capture once as full before the laptop can query any untrusted boundary.
		captured, err = e.captureSpool(caller, true, captured.heads, captured.result.DefaultRef, done)
		if err != nil {
			return api.PullExport{}, err
		}
	}
	e.mu.Lock()
	e.size = captured.size
	e.offset = 0
	e.heads = captured.heads
	e.defaultRef = captured.result.DefaultRef
	e.full = captured.full
	e.ready = true
	e.last = e.manager.options.Now()
	e.mu.Unlock()
	return e.description(false), nil
}

func foreignPrerequisites(required, seeds []string) bool {
	trusted := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		trusted[seed] = true
	}
	for _, oid := range required {
		if !trusted[oid] {
			return true
		}
	}
	return false
}

func sameHeads(a, b []proto.BundleRef) bool {
	if len(a) != len(b) {
		return false
	}
	for at := range a {
		if a[at] != b[at] {
			return false
		}
	}
	return true
}

func (e *export) description(empty bool) api.PullExport {
	e.mu.Lock()
	defer e.mu.Unlock()
	return api.PullExport{ID: e.id, Primary: e.primary, Token: e.token, Full: e.full, Empty: empty, Size: e.size, SeedHeads: append([]string(nil), e.seeds...), DefaultRef: e.defaultRef}
}

type readReservation struct {
	offset         int64
	request, epoch uint64
	prepared, run  context.Context
}

func (e *export) reserveRead(ctx context.Context, offset int64, request uint64) (*readReservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.life.Err() != nil || e.closing || e.capturing || !e.ready || e.readBusy || offset < 0 || offset != e.offset {
		return nil, exportError("pull_protocol", "pull.read offset or ownership state is invalid")
	}
	ticket := &readReservation{offset: offset, request: request, epoch: e.epoch, prepared: ctx}
	e.readTicket, e.readBusy = ticket, true
	return ticket, nil
}

func (e *export) read(ctx context.Context, ticket *readReservation) (api.PullChunk, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readTicket != ticket || !e.readBusy || e.closing || e.capturing || !e.ready || ticket.epoch != e.epoch || ticket.offset != e.offset {
		return api.PullChunk{}, exportError("pull_protocol", "pull.read offset or ownership state is invalid")
	}
	defer func() { e.readBusy = false }()
	ticket.run = ctx
	if err := ticket.prepared.Err(); err != nil {
		return api.PullChunk{}, err
	}
	if err := ctx.Err(); err != nil {
		return api.PullChunk{}, err
	}
	if err := e.life.Err(); err != nil {
		return api.PullChunk{}, err
	}
	offset := ticket.offset
	e.last = e.manager.options.Now()
	if offset == e.size {
		return api.PullChunk{Next: offset, EOF: true}, nil
	}
	data := make([]byte, min(int64(api.PullReadMax), e.size-offset))
	n, err := e.file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return api.PullChunk{}, fmt.Errorf("read owned pull spool: %w", err)
	}
	if n != len(data) {
		return api.PullChunk{}, exportError("pull_protocol", "the sealed pull spool ended early")
	}
	e.offset += int64(n)
	return api.PullChunk{Data: data, Next: e.offset, EOF: e.offset == e.size}, nil
}

func (e *export) removeSpool() {
	e.mu.Lock()
	file, path := e.file, e.path
	e.file = nil
	e.path = ""
	e.size = 0
	e.offset = 0
	e.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if path != "" {
		_ = os.Remove(path)
	}
}

func (e *export) close() {
	e.once.Do(func() {
		e.mu.Lock()
		e.closing = true
		done := e.workDone
		if !e.workStarted && !e.workClosed {
			e.workClosed = true
			e.capturing = false
			close(done)
		}
		e.mu.Unlock()
		e.cancel()
		<-done
		e.mu.Lock()
		source := e.source
		e.mu.Unlock()
		if source != nil {
			_ = source.Close()
		}
		e.removeSpool()
		e.manager.mu.Lock()
		delete(e.manager.exports, e.token)
		if e.manager.primary[e.primary] == e {
			delete(e.manager.primary, e.primary)
		}
		e.manager.mu.Unlock()
		close(e.closed)
	})
	<-e.closed
}
