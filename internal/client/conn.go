package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// ErrClosed is returned after the connection to the host is gone.
var ErrClosed = errors.New("the connection to the host closed")

// Conn is one bridge connection to a host: the helper's bridge, run over the
// host's transport (usually `ssh -T`), carrying framed requests, an attach
// stream and uploads.
type Conn struct {
	Host     Host
	Features []string

	hostID        string
	hostIDChanged bool

	proc   Proc
	cancel context.CancelFunc
	hint   string
	w      *proto.Writer
	stdin  io.WriteCloser
	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan proto.Response
	// uploading holds a token while an upload is on its way, from its first
	// frame to its answer (uploadTurn). mu guards the field.
	uploading chan struct{}
	stream    chan proto.Frame // attach frames: Attached, Output, Ended, Error
	err       error

	lastRecv atomic.Int64
	done     chan struct{}

	// RoundTrip's pings (roundtrip.go): those waiting for their pong, under
	// mu, and whether the bridge answers pings at all.
	timedPings    map[uint64]chan time.Time
	nextTimedPing atomic.Uint64
	noPong        atomic.Bool

	tr *connTrace // the debug trace (trace.go), or nil
}

// ErrNotInstalled means the host has no helper of this client's version.
var ErrNotInstalled = errors.New("the helper is not installed")

// HostID returns the verified host identity from the bridge hello.
func (c *Conn) HostID() string { return c.hostID }

// HostIDChanged reports a change from the previously saved identity.
func (c *Conn) HostIDChanged() bool { return c.hostIDChanged }

// startupExitWait bounds the grace after the transport closes its stream.
var startupExitWait = 500 * time.Millisecond

// Dial starts the bridge on the host and does the hello. For a saved host (one
// the user added, and so agreed to the helper on), a missing helper of this
// version is installed first: an upgrade never needs the user.
//
// A host that a provider made is asked for its state first, and started or
// resumed when it is stopped, unless ctx comes from WithoutWake. In a ctx
// from WithoutInstall, a missing helper is an error, and nothing is
// installed or pruned.
func Dial(ctx context.Context, h Host, interactive bool) (*Conn, error) {
	if _, err := ResolveHostTier(ctx, h); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !interactive {
		ctx = quietConnectionNotes(ctx)
	}
	if !installAllowed(ctx) {
		// A client built with go install learns the version of its helper
		// when it loads the helpers, which only Probe does otherwise. Load
		// them here, from this computer's cache only, so that the dial
		// starts the right helper, and never waits on a download. With
		// nothing cached, the dial goes on with the version it has, and a
		// missing helper is helper_missing, whose Try: line is host
		// upgrade, which downloads them.
		_ = helpers.Cached(version.Version, CacheDir())
	}
	r, err := routeTo(ctx, h, interactive)
	if err != nil {
		return nil, err
	}
	if err := pinRoute(ctx, h, r.t, interactive); err != nil {
		// A stopped cloud host cannot show its key either: start it first.
		if !mayWake(ctx, h, err) {
			return nil, err
		}
		if werr := wake(ctx, h); werr != nil {
			return nil, werr
		}
		if err := pinRoute(ctx, h, r.t, interactive); err != nil {
			return nil, err
		}
	}
	c, err := dial(ctx, h, r)
	if mayWake(ctx, h, err) {
		// A stopped cloud host: start it through its provider script, then try again.
		if werr := wake(ctx, h); werr != nil {
			return nil, werr
		}
		c, err = dial(ctx, h, r)
	}
	if !errors.Is(err, ErrNotInstalled) {
		return c, err
	}
	if _, ferr := FindHost(h.Name); ferr != nil {
		return nil, &UserError{Msg: fmt.Sprintf("the %s helper is not installed on %s", brand.Name, h.Name), Hint: brand.Name + " host add " + h.Name, Code: "helper_missing"}
	}
	if !installAllowed(ctx) {
		return nil, &UserError{Msg: fmt.Sprintf("the helper %s is not on %s", HelperVersion(), h.Name), Hint: brand.Name + " host upgrade " + h.Name, Code: "helper_missing"}
	}
	p, perr := Probe(ctx, r.t)
	if perr != nil {
		return nil, perr
	}
	if !p.Installed {
		if ierr := Install(ctx, r.t, p); ierr != nil {
			return nil, ierr
		}
	}
	return dial(ctx, h, r)
}

// mayWake reports whether err, from a connection to h, is one that waking
// h through its provider script may cure: h could not be reached, and its
// script wakes it on attach.
func mayWake(ctx context.Context, h Host, err error) bool {
	var ue *UserError
	return err != nil && h.Provider != nil && h.Provider.WakeOnAttach && wakeAllowed(ctx) &&
		errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "cannot reach")
}

func dial(ctx context.Context, h Host, rt route) (*Conn, error) {
	remote := RemoteHelper(HelperVersion()) + " bridge"
	ka, _ := rt.t.(KeepAliver)
	if ka != nil {
		// This transport does not end the bridge when the client goes, so the
		// bridge ends itself when the pings stop.
		remote += fmt.Sprintf(" --idle-exit %d", int(BridgeIdleExit.Seconds()))
	}
	// Startup follows the caller until the host sends a valid hello. The
	// connection then owns the process context, including its cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopStartup := context.AfterFunc(ctx, cancel)
	defer stopStartup()
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()
	proc, err := dialStart(procCtx, rt.t, remote)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	stdin := proc.Stdin()
	c := &Conn{Host: h, proc: proc, cancel: cancel, hint: rt.t.ReachHint(), stdin: stdin, w: proto.NewWriter(stdin),
		pending: map[uint64]chan proto.Response{}, stream: make(chan proto.Frame, 4096), done: make(chan struct{}),
		tr: newConnTrace(ctx)}
	r := bufio.NewReaderSize(proc.Stdout(), 256<<10)
	if err := c.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client"}); err != nil {
		return nil, c.startError(ctx, err)
	}
	type res struct {
		f   proto.Frame
		err error
	}
	first := make(chan res, 1)
	go func() { f, err := proto.Read(r); first <- res{f, err} }()
	select {
	case <-ctx.Done():
		c.killStartup()
		return nil, ctx.Err()
	case x := <-first:
		if x.err != nil {
			if !errors.Is(x.err, io.EOF) {
				c.kill()
			}
			return nil, c.startError(ctx, x.err)
		}
		if x.f.Type != proto.THello {
			c.killStartup()
			return nil, errors.New("unexpected first frame from host")
		}
		var hello proto.Hello
		if err := json.Unmarshal(x.f.Payload, &hello); err != nil {
			c.killStartup()
			return nil, fmt.Errorf("read the host's hello: %w", err)
		}
		if err := hello.Check(version.Protocol); err != nil {
			c.killStartup()
			return nil, &UserError{Msg: CleanLine(err.Error()), Hint: brand.Name + " host add " + h.Name + " (reinstalls the helper)", Code: "protocol"}
		}
		if hello.HostID != "" && !ValidHostID(hello.HostID) {
			c.killStartup()
			return nil, &UserError{Msg: fmt.Sprintf("the helper on %s sent an invalid host ID", h.Name), Hint: brand.Name + " host doctor " + h.Name, Code: "protocol"}
		}
		changed, err := noteHostID(ctx, h, hello.HostID)
		if err != nil {
			c.killStartup()
			return nil, err
		}
		c.hostID = hello.HostID
		c.hostIDChanged = changed
		c.Features = hello.Features
	}
	stopStartup()
	if err := ctx.Err(); err != nil {
		c.killStartup()
		return nil, err
	}
	started = true
	c.lastRecv.Store(time.Now().UnixNano())
	go c.readLoop(r)
	c.startBridgeDebug(ctx)
	go func() {
		defer cancel()
		_ = proc.Wait()
		c.fail(ErrClosed)
	}()
	if ka != nil {
		go c.keepAlive(ka.KeepAlive())
	}
	if l, ok := rt.lc.(Leaser); ok {
		go keepLease(context.WithoutCancel(ctx), c, l, leasePeriod)
	}
	return c, nil
}

// killStartup stops an invalid or cancelled handshake at once. Wait still
// reaps the transport and releases its private diagnostic files.
func (c *Conn) killStartup() {
	c.kill()
	go func() { _ = c.proc.Wait() }()
}

// waitStartup lets a transport report its exit after closing stdout. SSH and
// container transports can close the stream before collecting that status.
// Killing them first would replace a useful exit code with a local signal.
func (c *Conn) waitStartup(ctx context.Context) error {
	_ = c.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- c.proc.Wait() }()
	timer := time.NewTimer(startupExitWait)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c.kill()
		return ctx.Err()
	case <-timer.C:
		c.kill()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Conn) readLoop(r *bufio.Reader) {
	for {
		f, err := proto.Read(r)
		if err != nil {
			c.fail(ErrClosed)
			return
		}
		c.lastRecv.Store(time.Now().UnixNano())
		switch f.Type {
		case proto.TResponse:
			var resp proto.Response
			if json.Unmarshal(f.Payload, &resp) == nil {
				c.mu.Lock()
				ch := c.pending[resp.ID]
				delete(c.pending, resp.ID)
				c.mu.Unlock()
				if ch != nil {
					ch <- resp
				}
			}
		case proto.TPong:
			c.pong(f.Payload)
		case proto.TAttached, proto.TOutput, proto.TSnapshot, proto.TEnded, proto.TError:
			if c.tr != nil && f.Type == proto.TOutput {
				c.tr.arrived(f.Payload)
			}
			select {
			case c.stream <- f:
			case <-c.done:
				return
			}
		default:
			// Other frames are not for this connection.
		}
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

func (c *Conn) kill() {
	if c.cancel != nil {
		c.cancel()
	}
	c.proc.Kill()
}

// Close ends the connection.
func (c *Conn) Close() {
	if c.cancel != nil {
		defer c.cancel()
	}
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(500 * time.Millisecond):
		c.kill()
	}
	c.fail(ErrClosed)
}

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// LastRecv is when the last frame arrived.
func (c *Conn) LastRecv() time.Time { return time.Unix(0, c.lastRecv.Load()) }

// Ping sends a heartbeat. With the debug trace on, it is a timed ping.
func (c *Conn) Ping() error {
	if c.tr != nil {
		return c.w.Write(proto.TPing, c.tr.t.ping())
	}
	return c.w.Write(proto.TPing, nil)
}
