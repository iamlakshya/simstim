package egress

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
)

// One actual originRequest already owns this native TLS connection and turn.
// This adds delivery observation only, and cannot dial or authorise a route.
type physicalH2UploadConn struct {
	net.Conn
	native     net.Conn
	turn       *ModelUsageTurnV1
	mu         sync.Mutex
	closed     bool
	writes     sync.WaitGroup
	writeMu    sync.Mutex
	once       sync.Once
	closeErr   error
	preface    int
	header     [9]byte
	headerN    int
	reader     bytes.Reader
	framer     *http2.Framer
	frame      http2.FrameHeader
	remaining  uint32
	offset     uint32
	pad        uint32
	padKnown   bool
	bodyStream uint32
	h1         *physicalHTTP1Body
}

func (*physicalH2UploadConn) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newPhysicalH2UploadConn(scope *originRequest, turn *ModelUsageTurnV1, actual *tls.Conn) (*physicalH2UploadConn, error) {
	return newPreparedPhysicalH2UploadConn(scope, turn, actual, false)
}

func newPreparedPhysicalH2UploadConn(scope *originRequest, turn *ModelUsageTurnV1, actual *tls.Conn, prepared bool) (*physicalH2UploadConn, error) {
	if scope == nil || scope.s == nil || scope.ctx == nil || scope.ctx.Err() != nil || turn == nil || actual == nil {
		return nil, errPhysicalH2Binding
	}
	scope.mu.Lock()
	bound := !scope.closed && scope.conn == actual.NetConn() && scope.model != nil && scope.model.usage == turn
	scope.mu.Unlock()
	if !bound || !scope.allowed(scope.s.currentEngine(), scope.s.dialer) {
		return nil, errPhysicalH2Binding
	}
	return newPhysicalTLSUploadConn(turn, actual, scope.request.Host, prepared)
}

// The fixed-endpoint Judge and ordinary request owners share observation,
// after their own original dial, Source and permission checks. This function
// accepts an existing verified TLS connection and cannot create a route.
func newPhysicalTLSUploadConn(turn *ModelUsageTurnV1, actual *tls.Conn, host string, prepared bool) (*physicalH2UploadConn, error) {
	if turn == nil || actual == nil || host == "" {
		return nil, errPhysicalH2Binding
	}
	state := actual.ConnectionState()
	if !state.HandshakeComplete || (state.NegotiatedProtocol != http2.NextProtoTLS && state.NegotiatedProtocol != "http/1.1" && state.NegotiatedProtocol != "") || state.Version < tls.VersionTLS12 || len(state.VerifiedChains) == 0 || state.ServerName != host {
		return nil, errPhysicalH2Binding
	}
	if err := preparePhysicalUploadTurn(turn, prepared); err != nil {
		return nil, err
	}
	conn := &physicalH2UploadConn{Conn: actual, native: actual.NetConn(), turn: turn}
	if state.NegotiatedProtocol != http2.NextProtoTLS {
		conn.h1 = &physicalHTTP1Body{request: true, observe: func(payload []byte) { turn.physicalRequestPrefixV1(uint64(len(payload))) }}
	}
	conn.framer = http2.NewFramer(io.Discard, &conn.reader)
	conn.framer.SetMaxReadFrameSize((1 << 24) - 1)
	return conn, nil
}

// Close is the original connection owner's ordered writer join. The turn must
// not finish until this returns, including an early upstream response or reset.
func (c *physicalH2UploadConn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.closeErr = c.native.Close()
		c.writes.Wait()
	})
	return c.closeErr
}

func (c *physicalH2UploadConn) Write(payload []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	c.writes.Add(1)
	c.mu.Unlock()
	defer c.writes.Done()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	count, err := c.Conn.Write(payload)
	if count > 0 {
		if observeErr := c.observe(payload[:count]); observeErr != nil {
			return count, observeErr
		}
	}
	return count, err //nolint:wrapcheck // retain the actual TLS successful-write prefix
}

func (c *physicalH2UploadConn) observe(payload []byte) error {
	if c.h1 != nil {
		return c.h1.feed(payload)
	}
	for len(payload) != 0 {
		if c.preface < len(http2.ClientPreface) {
			count := min(len(payload), len(http2.ClientPreface)-c.preface)
			if string(payload[:count]) != http2.ClientPreface[c.preface:c.preface+count] {
				return errPhysicalH2Binding
			}
			c.preface += count
			payload = payload[count:]
			continue
		}
		if c.headerN < len(c.header) {
			count := copy(c.header[c.headerN:], payload)
			c.headerN += count
			payload = payload[count:]
			if c.headerN != len(c.header) {
				continue
			}
			c.reader.Reset(c.header[:])
			frame, err := c.framer.ReadFrameHeader()
			if err != nil {
				return err
			}
			c.frame, c.remaining, c.offset = frame, frame.Length, 0
			c.pad, c.padKnown = 0, false
			if frame.Type == http2.FrameHeaders && c.bodyStream == 0 {
				c.bodyStream = frame.StreamID
			}
		}
		if c.remaining != 0 {
			count := min(len(payload), int(c.remaining))
			if count == 0 {
				// A successful write may end exactly at the native frame
				// header. Retain its original payload/padding state until
				// another successful prefix actually supplies those bytes.
				return nil
			}
			if c.frame.Type == http2.FrameData {
				if c.bodyStream == 0 || c.frame.StreamID != c.bodyStream {
					return errPhysicalH2Binding
				}
				start, end := uint32(0), c.frame.Length
				if c.frame.Flags.Has(http2.FlagDataPadded) {
					if !c.padKnown {
						c.pad, c.padKnown = uint32(payload[0]), true
						if c.pad+1 > c.frame.Length {
							return errPhysicalH2Binding
						}
					}
					start, end = 1, end-c.pad
				}
				from, to := max(start, c.offset), min(end, c.offset+uint32(count))
				if from < to {
					c.turn.physicalRequestPrefixV1(uint64(to - from))
				}
			}
			c.remaining -= uint32(count)
			c.offset += uint32(count)
			payload = payload[count:]
		}
		if c.remaining == 0 {
			c.headerN = 0
		}
	}
	return nil
}

func preparePhysicalUploadTurn(turn *ModelUsageTurnV1, prepared bool) error {
	if turn == nil {
		return errPhysicalH2Binding
	}
	if prepared {
		turn.mu.Lock()
		ready := turn.physicalRequest && turn.requestBytes == 0 && !turn.finished
		turn.mu.Unlock()
		if !ready {
			return errPhysicalH2Binding
		}
		return nil
	}
	return turn.beginPhysicalRequestV1()
}

// Existing configured plaintext origins retain their original HTTP1 path.
// As with TLS, the caller has already admitted and attached this exact socket.
func newPhysicalHTTP1UploadConn(turn *ModelUsageTurnV1, actual net.Conn, prepared bool) (*physicalH2UploadConn, error) {
	if actual == nil {
		return nil, errPhysicalH2Binding
	}
	if err := preparePhysicalUploadTurn(turn, prepared); err != nil {
		return nil, err
	}
	conn := &physicalH2UploadConn{Conn: actual, native: actual, turn: turn}
	conn.h1 = &physicalHTTP1Body{request: true, observe: func(payload []byte) { turn.physicalRequestPrefixV1(uint64(len(payload))) }}
	return conn, nil
}
