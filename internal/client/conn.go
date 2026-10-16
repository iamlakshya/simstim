package client

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
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

	proc   Proc
	hint   string
	w      *proto.Writer
	stdin  io.WriteCloser
	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan proto.Response
	stream  chan proto.Frame // attach frames: Attached, Output, Ended, Error
	err     error

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

// Dial starts the bridge on the host and does the hello. For a saved host (one
// the user added, and so agreed to the helper on), a missing helper of this
// version is installed first: an upgrade never needs the user.
//
// A host that a provider made is asked for its state first, and started or
// resumed when it is stopped, unless ctx comes from WithoutWake.
func Dial(ctx context.Context, h Host, interactive bool) (*Conn, error) {
	r, err := routeTo(ctx, h, interactive)
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, h, r)
	var ue *UserError
	if err != nil && h.Provider != nil && h.Provider.WakeOnAttach && errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "cannot reach") {
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
		return nil, &UserError{Msg: fmt.Sprintf("the %s helper is not installed on %s", brand.Name, h.Name), Hint: brand.Name + " host add " + h.Name}
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

func dial(ctx context.Context, h Host, rt route) (*Conn, error) {
	remote := RemoteHelper(HelperVersion()) + " bridge"
	ka, _ := rt.t.(KeepAliver)
	if ka != nil {
		// This transport does not end the bridge when the client goes, so the
		// bridge ends itself when the pings stop.
		remote += fmt.Sprintf(" --idle-exit %d", int(BridgeIdleExit.Seconds()))
	}
	// The bridge lives as long as the connection, not as long as this dial:
	// it keeps ctx's values but not its deadline.
	proc, err := rt.t.Start(context.WithoutCancel(ctx), remote)
	if err != nil {
		return nil, err
	}
	stdin := proc.Stdin()
	c := &Conn{Host: h, proc: proc, hint: rt.t.ReachHint(), stdin: stdin, w: proto.NewWriter(stdin),
		pending: map[uint64]chan proto.Response{}, stream: make(chan proto.Frame, 4096), done: make(chan struct{}),
		tr: newConnTrace(ctx)}
	r := bufio.NewReaderSize(proc.Stdout(), 256<<10)
	if err := c.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client"}); err != nil {
		c.kill()
		return nil, c.startError(err)
	}
	type res struct {
		f   proto.Frame
		err error
	}
	first := make(chan res, 1)
	go func() { f, err := proto.Read(r); first <- res{f, err} }()
	select {
	case <-ctx.Done():
		c.kill()
		return nil, ctx.Err()
	case x := <-first:
		if x.err != nil {
			c.kill()
			return nil, c.startError(x.err)
		}
		if x.f.Type != proto.THello {
			c.kill()
			return nil, errors.New("unexpected first frame from host")
		}
		var hello proto.Hello
		if err := json.Unmarshal(x.f.Payload, &hello); err != nil {
			c.kill()
			return nil, fmt.Errorf("read the host's hello: %w", err)
		}
		if err := hello.Check(version.Protocol); err != nil {
			c.kill()
			return nil, &UserError{Msg: err.Error(), Hint: brand.Name + " host add " + h.Name + "   (reinstalls the helper)"}
		}
		c.Features = hello.Features
	}
	c.lastRecv.Store(time.Now().UnixNano())
	go c.readLoop(r)
	go func() {
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

func (c *Conn) startError(err error) error {
	waitErr := c.proc.Wait()
	msg := strings.TrimSpace(c.proc.Stderr())
	if strings.Contains(msg, "No such file") || strings.Contains(msg, "not found") {
		return ErrNotInstalled
	}
	if msg == "" {
		return err
	}
	if lines := strings.Split(msg, "\n"); len(lines) > 3 {
		msg = strings.Join(lines[len(lines)-3:], "\n")
	}
	if errors.Is(waitErr, ErrUnreachable) {
		return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", c.Host.Name, msg), Hint: c.hint}
	}
	return &UserError{Msg: fmt.Sprintf("the helper on %s did not start: %s", c.Host.Name, msg), Hint: brand.Name + " host doctor " + c.Host.Name}
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

func (c *Conn) kill() { c.proc.Kill() }

// Close ends the connection.
func (c *Conn) Close() {
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

// Call sends a request and decodes the result into out.
func (c *Conn) Call(ctx context.Context, op string, args any, out any) error {
	id := c.nextID.Add(1)
	req := proto.Request{ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("encode the %s request: %w", op, err)
		}
		req.Args = b
	}
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.w.WriteJSON(proto.TRequest, req); err != nil {
		return ErrClosed
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if !resp.OK {
			if resp.Error != nil {
				return &UserError{Msg: resp.Error.Message, Hint: resp.Error.Hint, Code: resp.Error.Code, Data: resp.Error.Data}
			}
			return errors.New("the host refused the request")
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("read the answer to %s: %w", op, err)
			}
		}
		return nil
	}
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

// Upload sends a file into a session's paste folder and returns its path on the host.
func (c *Conn) Upload(ctx context.Context, session, name string, data []byte) (string, error) {
	id := c.nextID.Add(1)
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return "", c.err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.w.WriteJSON(proto.TUploadBegin, proto.UploadBegin{ID: id, Session: session, Name: name, Size: int64(len(data))}); err != nil {
		return "", ErrClosed
	}
	for off := 0; off < len(data); off += 256 << 10 {
		end := off + 256<<10
		if end > len(data) {
			end = len(data)
		}
		if err := c.w.Write(proto.TUploadChunk, proto.EncodeChunk(id, data[off:end])); err != nil {
			return "", ErrClosed
		}
	}
	sum := sha256.Sum256(data)
	if err := c.w.WriteJSON(proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: hex.EncodeToString(sum[:])}); err != nil {
		return "", ErrClosed
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return "", ErrClosed
		}
		if !resp.OK {
			if resp.Error != nil {
				return "", &UserError{Msg: resp.Error.Message, Hint: resp.Error.Hint}
			}
			return "", errors.New("upload failed")
		}
		var r proto.UploadResult
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			return "", fmt.Errorf("read the answer to an upload: %w", err)
		}
		return r.Path, nil
	}
}
