package egress

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

type physicalH2Conn struct {
	actual         *tls.Conn
	plainWrite     func([]byte) (int, error) // constructor binds the actual TLS writer; tests may inject a real-prefix cut
	owner          *physicalH2Owner
	ctx            context.Context
	cancel         context.CancelFunc
	readMu         sync.Mutex
	inRaw          bytes.Buffer
	inFrames       *http2.Framer
	inReader       bytes.Reader
	inHeaderReader bytes.Reader
	inHeaderFrames *http2.Framer
	controlMu      sync.Mutex
	controls       []uint32
	credit         uint64
	readDeadline   time.Time
	frameReady     bool
	readPending    []byte
	prefaced       bool
	headers        *physicalH2HeaderGuard
	newHeader      uint32
	writeMu        sync.Mutex
	writes         sync.WaitGroup
	output         physicalH2Output
	closeOnce      sync.Once
	closeErr       error
}

func newPhysicalH2Conn(ctx context.Context, actual *tls.Conn) (*physicalH2Conn, error) {
	if ctx == nil || actual == nil || ctx.Err() != nil {
		return nil, errPhysicalH2Binding
	}
	state := actual.ConnectionState()
	if !state.HandshakeComplete || state.NegotiatedProtocol != http2.NextProtoTLS {
		return nil, errPhysicalH2Binding
	}
	owner, err := newPhysicalH2Owner(state)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	conn := &physicalH2Conn{actual: actual, plainWrite: actual.Write, owner: owner, ctx: ctx, cancel: cancel, headers: newPhysicalH2HeaderGuard()}
	conn.inFrames = http2.NewFramer(io.Discard, &conn.inReader)
	conn.inHeaderFrames = http2.NewFramer(io.Discard, &conn.inHeaderReader)
	owner.injectReset, owner.writeReset = conn.requestReset, conn.writeReset
	conn.inFrames.SetMaxReadFrameSize(physicalH2OriginalFrameLimit)
	conn.output.init()
	return conn, nil
}

// Deliberately no ConnectionState method: the public standard server must see
// its UnencryptedHTTP2 input mode, never attempt another TLS handshake.
func (c *physicalH2Conn) LocalAddr() net.Addr  { return c.actual.LocalAddr() }
func (c *physicalH2Conn) RemoteAddr() net.Addr { return c.actual.RemoteAddr() }
func (c *physicalH2Conn) SetDeadline(at time.Time) error {
	c.controlMu.Lock()
	defer c.controlMu.Unlock()
	c.readDeadline = at
	err := c.actual.SetDeadline(at)
	if len(c.controls) != 0 || c.credit != 0 {
		_ = c.actual.SetReadDeadline(time.Now())
	}
	return err
}
func (c *physicalH2Conn) SetReadDeadline(at time.Time) error {
	c.controlMu.Lock()
	defer c.controlMu.Unlock()
	c.readDeadline = at
	if len(c.controls) != 0 || c.credit != 0 {
		at = time.Now()
	}
	return c.actual.SetReadDeadline(at)
}
func (c *physicalH2Conn) SetWriteDeadline(at time.Time) error { return c.actual.SetWriteDeadline(at) }

func (c *physicalH2Conn) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.readPending) == 0 {
		if err := c.ctx.Err(); err != nil {
			return 0, err
		}
		if !c.headers.open {
			if control := c.nextReset(); len(control) != 0 {
				c.readPending = control
				continue
			}
		}
		if !c.prefaced {
			preface := make([]byte, len(http2.ClientPreface))
			if _, err := io.ReadFull(c.actual, preface); err != nil || string(preface) != http2.ClientPreface {
				return 0, errPhysicalH2Binding
			}
			c.prefaced, c.readPending = true, preface
			continue
		}
		frame, err := c.readPhysicalFrame()
		if err == errPhysicalH2ReadInterrupt {
			continue
		}
		if err != nil {
			return 0, err
		}
		raw := c.inRaw.Bytes()
		switch frame := frame.(type) {
		case *http2.HeadersFrame:
			stream, fresh, err := c.owner.introduce(c.ctx, frame.StreamID)
			if err != nil {
				return 0, err
			}
			c.newHeader = 0
			if fresh {
				c.newHeader = frame.StreamID
			}
			if err := c.headers.begin(frame.StreamID); err != nil {
				return 0, err
			}
			if err := c.headers.fragment(frame.StreamID, frame.HeaderBlockFragment(), frame.HeadersEnded()); err != nil {
				return 0, err
			}
			if !fresh && frame.HeadersEnded() && c.headers.truncated {
				// The old external limit still governs trailers. Reject only
				// oversized input before its final original fragment reaches
				// the internally enlarged standard decoder.
				return 0, http2.ConnectionError(http2.ErrCodeEnhanceYourCalm)
			}
			if stream != nil && frame.HeadersEnded() {
				c.owner.mu.Lock()
				stream.truncated = c.headers.truncated
				c.owner.mu.Unlock()
			}
			if frame.HeadersEnded() && c.newHeader != 0 {
				raw, err = c.appendToken(raw, c.newHeader)
				if err != nil {
					return 0, err
				}
			}
		case *http2.ContinuationFrame:
			if err := c.headers.fragment(frame.StreamID, frame.HeaderBlockFragment(), frame.HeadersEnded()); err != nil {
				return 0, err
			}
			if frame.HeadersEnded() && c.newHeader == 0 && c.headers.truncated {
				return 0, http2.ConnectionError(http2.ErrCodeEnhanceYourCalm)
			}
			if frame.HeadersEnded() && c.newHeader != 0 {
				c.owner.mu.Lock()
				if stream := c.owner.streams[c.newHeader]; stream != nil {
					stream.truncated = c.headers.truncated
				}
				c.owner.mu.Unlock()
				raw, err = c.appendToken(raw, c.newHeader)
				if err != nil {
					return 0, err
				}
			}
		case *http2.RSTStreamFrame:
			fence, err := c.resetFence(frame.StreamID)
			if err != nil {
				return 0, err
			}
			if len(fence) != 0 {
				raw = append(bytes.Clone(raw), fence...)
			}
		case *http2.PingFrame:
			if !frame.Flags.Has(http2.FlagPingAck) {
				if err := c.guestPing(frame.Data); err != nil {
					return 0, err
				}
			}
		}
		c.readPending = raw
		c.frameReady = true
	}
	count := copy(output, c.readPending)
	c.readPending = c.readPending[count:]
	return count, nil
}

func (c *physicalH2Conn) appendToken(raw []byte, stream uint32) ([]byte, error) {
	token, err := c.owner.key.literal(stream)
	if err != nil {
		return nil, err
	}
	return appendPhysicalH2TokenFinal(raw, token, stream)
}

func (c *physicalH2Conn) resetFence(id uint32) ([]byte, error) {
	o := c.owner
	o.mu.Lock()
	stream := o.streams[id]
	if stream == nil || stream.finished {
		o.mu.Unlock()
		return nil, nil
	}
	for _, pending := range o.fences {
		if pending == id {
			o.mu.Unlock()
			return nil, nil
		}
	}
	stream.reset = true
	stream.resetRequested = true
	if o.scheduler {
		cancel := stream.cancel
		o.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil, nil
	}
	cancel := stream.cancel
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for {
		var data [8]byte
		if _, err := io.ReadFull(rand.Reader, data[:]); err != nil {
			return nil, errPhysicalH2Binding
		}
		o.mu.Lock()
		_, private := o.fences[data]
		if !private && o.guestPings[data] == 0 {
			o.fences[data] = id
			o.mu.Unlock()
			var wire bytes.Buffer
			if err := http2.NewFramer(&wire, nil).WritePing(false, data); err != nil {
				return nil, err
			}
			return wire.Bytes(), nil
		}
		o.mu.Unlock()
	}
}

func (c *physicalH2Conn) guestPing(data [8]byte) error {
	for {
		c.owner.mu.Lock()
		_, private := c.owner.fences[data]
		if !private && c.owner.guestPingN < 10002 {
			c.owner.guestPings[data]++
			c.owner.guestPingN++
			c.owner.mu.Unlock()
			return nil
		}
		changed := c.owner.changed
		c.owner.mu.Unlock()
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-changed:
		}
	}
}
