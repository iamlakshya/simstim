package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// ErrClosed is returned after the connection to the host is gone.
var ErrClosed = errors.New("the connection to the host closed")

// Conn is one bridge connection to a host: the helper's bridge, run over the
// host's transport (usually `ssh -T`), carrying framed requests, an attach
// stream and uploads.
type Conn struct {
	Host     Host
	Features []string

	hostTransport  *hostTransportStamp
	hostID         string
	hostIDChanged  bool
	route          route
	agentOnly      bool
	agentReader    *bufio.Reader
	procWatch      sync.Once
	procDone       chan struct{}
	agentStopTime  sync.Once
	agentStoppedAt time.Time
	agentClose     sync.Once

	proc   Proc
	cancel context.CancelFunc
	hint   string
	w      *proto.Writer
	stdin  io.WriteCloser
	nextID atomic.Uint64

	mu        sync.Mutex
	pending   map[uint64]chan proto.Response
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

// HostID returns the 32-character hex host ID from the bridge's hello, or ""
// when an older helper sent none.
func (c *Conn) HostID() string { return c.hostID }

// HostIDChanged reports whether this connection observed a host ID different
// from the one previously stored in tiers.toml.
func (c *Conn) HostIDChanged() bool { return c.hostIDChanged }

// startupExitWait bounds the grace after the transport closes its stream.
var startupExitWait = 500 * time.Millisecond

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

func (c *Conn) watchProcess() {
	c.procWatch.Do(func() {
		go func() {
			if c.cancel != nil {
				defer c.cancel()
			}
			_ = c.proc.Wait()
			close(c.procDone)
			c.fail(ErrClosed)
		}()
	})
}

// closeAgentTransport interrupts only this connection's owned process and
// pipes. A link reader can call it without recursively joining itself.
func (c *Conn) closeAgentTransport() error {
	c.agentStopTime.Do(func() {
		at := time.Now()
		if source, ok := c.proc.(*sshProc); ok {
			at = source.revokeAgentAt(at)
		}
		c.agentStoppedAt = at
	})
	c.kill()
	if output, ok := c.proc.Stdout().(io.Closer); ok {
		_ = output.Close()
	}
	_ = c.stdin.Close()
	c.fail(ErrClosed)
	return nil
}

// Close ends the connection.
func (c *Conn) Close() {
	if c.agentOnly {
		c.agentClose.Do(func() {
			_ = c.closeAgentTransport()
			// Process output can need two seconds, followed by the native
			// log's single four-second scan/cleanup budget. Link teardown
			// overlaps this absolute deadline and repeated Close adds none.
			remaining := time.Until(c.agentStoppedAt.Add(3 * sshWaitDelay))
			timer := time.NewTimer(max(remaining, 0))
			defer timer.Stop()
			select {
			case <-c.procDone:
			case <-timer.C:
				if source, ok := c.proc.(*sshProc); ok {
					source.agentShutdownFailed()
				}
			}
		})
		return
	}
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

// Stream returns attach frames.
func (c *Conn) Stream() <-chan proto.Frame { return c.stream }

// Send writes a raw frame. With the debug trace on, it records each key.
func (c *Conn) Send(t proto.Type, p []byte) error {
	if c.tr != nil && t == proto.TInput {
		return c.tr.sendKey(c.w, p)
	}
	return c.w.Write(t, p)
}

// SendJSON writes a JSON frame.
func (c *Conn) SendJSON(t proto.Type, v any) error { return c.w.WriteJSON(t, v) }
