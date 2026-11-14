package host

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// cancelPendingStartOp can stop only the actual pending owner before launch.
// The running holder does not recognise it as a request to kill an agent.
const cancelPendingStartOp = "cancel_start"

// holderStartup owns the existing control endpoints before manifest loading.
// Startup runs synchronously. A pending stop cannot acknowledge completion
// until that owner returns, closes its resources and releases holder.lock.
type holderStartup struct {
	ctx    context.Context
	cancel context.CancelFunc
	peer   *Holder // immutable peer policy and logger, before a manifest exists

	mu             sync.Mutex
	active         *Holder
	ready          bool
	launching      bool
	closed         bool
	waiting        bool
	want           []string
	secretReady    chan struct{}
	secretResult   []string
	secretDeadline time.Time
	connections    map[net.Conn]struct{}
	stops          []*hclient
	stopIDs        []uint64
	accepts        sync.WaitGroup
	serves         sync.WaitGroup
}

func newHolderStartup(id string) *holderStartup {
	ctx, cancel := context.WithCancel(context.Background())
	return &holderStartup{ctx: ctx, cancel: cancel,
		connections: make(map[net.Conn]struct{}),
		peer:        &Holder{id: id, log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
}

func beginHolderStartup(id string) (*holderStartup, error) {
	p := newHolderStartup(id)
	if err := p.peer.listen(); err != nil {
		p.cancel()
		return nil, err
	}
	for _, endpoint := range []struct {
		ln   net.Listener
		hook bool
	}{{p.peer.ln, false}, {p.peer.hookLn, true}} {
		p.accepts.Add(1)
		go p.accept(endpoint.ln, endpoint.hook)
	}
	return p, nil
}

func (p *holderStartup) accept(ln net.Listener, hook bool) {
	defer p.accepts.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = conn.Close()
			return
		}
		p.connections[conn] = struct{}{}
		p.serves.Add(1)
		p.mu.Unlock()
		go p.serve(conn, hook)
	}
}

func pendingHolderError() *proto.Error {
	return &proto.Error{Code: "starting", Message: "the session is starting; its stop has not been confirmed", Hint: "Try again in a moment."}
}

func (p *holderStartup) reply(c *hclient, id uint64, value any, perr *proto.Error) {
	resp := proto.Response{ID: id, OK: perr == nil, Error: perr}
	if value != nil {
		resp.Result, _ = json.Marshal(value)
	}
	_ = c.w.WriteJSON(proto.TResponse, resp)
}

func (p *holderStartup) prepare(h *Holder, restored bool) {
	h.ln, h.hookLn, h.peerCheck = p.peer.ln, p.peer.hookLn, p.peer.peerCheck
	h.startup = p
	p.mu.Lock()
	p.ready = len(h.m.Secrets) == 0 || restored
	p.mu.Unlock()
}

func (p *holderStartup) awaitSecrets(want []string, timeout time.Duration) []string {
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		return nil
	}
	p.want, p.waiting, p.ready = append([]string(nil), want...), true, true
	p.secretDeadline = time.Now().Add(timeout)
	p.secretReady = make(chan struct{})
	p.secretResult = nil
	ready := p.secretReady
	deadline := p.secretDeadline
	p.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ready:
	case <-p.ctx.Done():
	case <-timer.C:
	}
	return p.finishSecretsWait()
}

// finishSecretsWait is the single delivery/timeout decision under the same
// gate as acknowledgement. A timer selection cannot discard an accepted value.
func (p *holderStartup) finishSecretsWait() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting, p.want = false, nil
	env := p.secretResult
	p.secretResult = nil
	if p.ctx.Err() != nil {
		return nil
	}
	return env
}

// beginAgent is the linearisation point between a cancellable pending start
// and the actual child-launch owner. It never holds the gate across IO.
func (p *holderStartup) beginAgent() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.launching = true
	return nil
}

func (p *holderStartup) activate(h *Holder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = h
}

// finish runs only after synchronous startup has returned. No pending stop
// can report success before every startup resource and holder.lock is gone.
func (p *holderStartup) finish(releaseLock func()) {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	for conn := range p.connections {
		_ = conn.Close()
	}
	p.mu.Unlock()
	p.peer.closeSockets()
	p.accepts.Wait()
	p.serves.Wait()
	releaseLock()
	for i, c := range p.stops {
		p.reply(c, p.stopIDs[i], map[string]bool{"killed": true}, nil)
		c.close()
	}
}

// loadStartingManifest leaves all filesystem work on the synchronous startup
// owner. A delayed read cannot outlive a confirmed pending stop.
var loadStartingManifest = func(ctx context.Context, id string) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := LoadManifest(id)
	if stopped := ctx.Err(); stopped != nil {
		return nil, stopped
	}
	return m, err
}
