package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func fwFrame(opcode byte, body ...byte) []byte {
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)+1))
	return append(append(frame, opcode), body...)
}

func fwString(s string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(s))), s...)
}

type fwWriterFunc func([]byte) (int, error)

func (w fwWriterFunc) Write(p []byte) (int, error) { return w(p) }

func fwBindFrame(name, key, session, signature string, forwarding byte) []byte {
	var body []byte
	for _, value := range []string{name, key, session, signature} {
		body = append(body, fwString(value)...)
	}
	return fwFrame(27, append(body, forwarding)...)
}

// These bytes test only the firewall's structural boundary. Setting the
// private verified bit in a fixture is not a collector or crypto proof.
func fwBinding() *peerAgentBinding {
	return &peerAgentBinding{wire: fwBindFrame("session-bind@openssh.com", "public-key", "session-id", "signature", 1),
		owner: &Conn{}, scope: agentbridge.Scope{Session: "abc23def", Lease: "0123456789abcdef0123456789abcdef"}, verified: true}
}

type fwMemoryConn struct {
	reader                                      io.Reader
	writer                                      io.Writer
	mu                                          sync.Mutex
	reads, closes, deadlines, maxRead, maxWrite int
	bodyBuffers                                 map[*byte]bool
	closed                                      bool
}

func (c *fwMemoryConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.maxRead = max(c.maxRead, len(p))
	if cap(p) == 32768 && len(p) > 0 {
		if c.bodyBuffers == nil {
			c.bodyBuffers = map[*byte]bool{}
		}
		c.bodyBuffers[&p[0]] = true
	}
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	return c.reader.Read(p)
}

func (c *fwMemoryConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.maxWrite = max(c.maxWrite, len(p))
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	return c.writer.Write(p)
}

func (c *fwMemoryConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	c.closed = true
	return nil
}

func (c *fwMemoryConn) SetDeadline(time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadlines++
	return nil // All fixture I/O is finite and synchronous.
}
func (c *fwMemoryConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *fwMemoryConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }
func (*fwMemoryConn) LocalAddr() net.Addr                  { return fwAddress("local") }
func (*fwMemoryConn) RemoteAddr() net.Addr                 { return fwAddress("source") }

type fwAddress string

func (fwAddress) Network() string  { return "agent-firewall-fixture" }
func (a fwAddress) String() string { return string(a) }

func fwMemoryRun(binding *peerAgentBinding, request, replies []byte) (remote, source *fwMemoryConn, output, upstream []byte, err error) {
	var toRemote, toSource bytes.Buffer
	remote = &fwMemoryConn{reader: bytes.NewReader(request), writer: &toRemote}
	source = &fwMemoryConn{reader: bytes.NewReader(replies), writer: &toSource}
	err = forwardAgentAuthentication(context.Background(), remote, source, binding)
	return remote, source, toRemote.Bytes(), toSource.Bytes(), err
}

func TestAgentFirewallRequiresVerifiedBoundOwnerBeforeAnyWrite(t *testing.T) {
	for _, name := range []string{"nil", "unverified", "owner", "scope", "empty", "short", "length", "opcode", "extension", "empty key", "empty session", "empty signature", "forwarding", "trailing"} {
		t.Run(name, func(t *testing.T) {
			binding := fwBinding()
			switch name {
			case "nil":
				binding = nil
			case "unverified":
				binding.verified = false
			case "owner":
				binding.owner = nil
			case "scope":
				binding.scope.Session = "BAD"
			case "empty":
				binding.wire = nil
			case "short":
				binding.wire = []byte{0, 0, 0}
			case "length":
				binding.wire[3]++
			case "opcode":
				binding.wire[4] = 11
			case "extension":
				binding.wire = fwBindFrame("query", "key", "sid", "sig", 1)
			case "empty key":
				binding.wire = fwBindFrame(agentFWSessionBind, "", "sid", "sig", 1)
			case "empty session":
				binding.wire = fwBindFrame(agentFWSessionBind, "key", "", "sig", 1)
			case "empty signature":
				binding.wire = fwBindFrame(agentFWSessionBind, "key", "sid", "", 1)
			case "forwarding":
				binding.wire = fwBindFrame(agentFWSessionBind, "key", "sid", "sig", 0)
			case "trailing":
				binding.wire = append(binding.wire, 0)
				binary.BigEndian.PutUint32(binding.wire[:4], uint32(len(binding.wire)-4))
			}
			remote, source, output, upstream, err := fwMemoryRun(binding, fwFrame(11), fwFrame(6))
			if !isAgentSourceFailure(err) || len(output) != 0 || len(upstream) != 0 || remote.reads != 0 || source.reads != 0 {
				t.Fatalf("invalid binding exposed protocol bytes: source=%d remote=%d err=%v", len(upstream), len(output), err)
			}
			if !remote.closed || !source.closed {
				t.Fatal("invalid binding retained an owned endpoint")
			}
		})
	}
}

func TestAgentFirewallNeverFallsBackWhenPrefixIsRejectedOrUnsupported(t *testing.T) {
	for name, ack := range map[string][]byte{
		"failure": fwFrame(5), "unsupported": fwFrame(28), "wrong opcode": fwFrame(12, 0, 0, 0, 0),
		"extra success body": fwFrame(6, 0), "zero body": {0, 0, 0, 0},
		"oversized": binary.BigEndian.AppendUint32(nil, 262145), "truncated": {0, 0, 0},
		"missing opcode": {0, 0, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			binding := fwBinding()
			remote, _, output, upstream, err := fwMemoryRun(binding, fwFrame(11), ack)
			if !isAgentSourceFailure(err) || !bytes.Equal(upstream, binding.wire) || len(output) != 0 || remote.reads != 0 {
				t.Fatalf("prefix failure reached an authentication request: source=%d remote=%d err=%v", len(upstream), len(output), err)
			}
		})
	}
}

func TestAgentFirewallInjectsThePrefixOnEveryFreshSource(t *testing.T) {
	binding := fwBinding()
	original := bytes.Clone(binding.wire)
	request, reply := fwFrame(11), fwFrame(12, 0, 0, 0, 0)
	for range 3 {
		_, _, output, upstream, err := fwMemoryRun(binding, request, append(fwFrame(6), reply...))
		if err != nil || !bytes.Equal(output, reply) || !bytes.Equal(upstream, append(bytes.Clone(original), request...)) {
			t.Fatalf("fresh source lacked its own prefix: source=%d reply=%d err=%v", len(upstream), len(output), err)
		}
	}
	if !bytes.Equal(binding.wire, original) {
		t.Fatal("firewall modified the shared immutable binding")
	}
}

func TestAgentFirewallDeniesEveryOpcodeExceptListSignAndExactBind(t *testing.T) {
	for value := 0; value < 256; value++ {
		t.Run(fmt.Sprintf("opcode-%d", value), func(t *testing.T) {
			opcode := byte(value)
			binding := fwBinding()
			request := fwFrame(opcode, []byte("denied operation")...)
			expected := fwFrame(5)
			upstream := bytes.Clone(binding.wire)
			replies := fwFrame(6)
			switch opcode {
			case 11:
				request = fwFrame(11)
				expected = fwFrame(12, 0, 0, 0, 0)
				upstream = append(upstream, request...)
				replies = append(replies, expected...)
			case 13:
				request = fwFrame(13, []byte("opaque sign request")...)
				expected = fwFrame(14, fwString("signature")...)
				upstream = append(upstream, request...)
				replies = append(replies, expected...)
			case 27:
				request = fwFrame(27, append(fwString("query"), []byte("private extension")...)...)
				expected = fwFrame(28)
			}
			_, _, output, sent, err := fwMemoryRun(binding, request, replies)
			if err != nil || !bytes.Equal(output, expected) || !bytes.Equal(sent, upstream) {
				t.Fatalf("opcode %d escaped policy or lost alignment: source=%d response=%d err=%v", opcode, len(sent), len(output), err)
			}
		})
	}
}

func TestAgentFirewallChecksTheWholeExtensionNameBeforeForwarding(t *testing.T) {
	short := agentFWSessionBind[:len(agentFWSessionBind)-1]
	for _, name := range []string{"", "query", short, agentFWSessionBind + "X", short + "X", "S" + agentFWSessionBind[1:], short + "\x00"} {
		binding := fwBinding()
		request := fwFrame(27, append(fwString(name), []byte("opaque extension body")...)...)
		// The denied extension is followed by a canonical List on the same stream.
		request = append(request, fwFrame(11)...)
		list := fwFrame(12, 0, 0, 0, 0)
		_, _, output, upstream, err := fwMemoryRun(binding, request, append(fwFrame(6), list...))
		if err != nil || !bytes.Equal(output, append(fwFrame(28), list...)) || !bytes.Equal(upstream, append(bytes.Clone(binding.wire), fwFrame(11)...)) {
			t.Fatalf("extension name bypassed validation or broke following List: name length=%d err=%v", len(name), err)
		}
	}
	for _, reply := range []byte{6, 5, 28} {
		binding := fwBinding()
		downstream := fwBindFrame(agentFWSessionBind, "destination-key", "destination-session", "destination-signature", 0)
		_, _, output, upstream, err := fwMemoryRun(binding, downstream, append(fwFrame(6), fwFrame(reply)...))
		if err != nil || !bytes.Equal(output, fwFrame(reply)) || !bytes.Equal(upstream, append(bytes.Clone(binding.wire), downstream...)) {
			t.Fatalf("exact downstream bind or its response was rejected: opcode=%d err=%v", reply, err)
		}
	}
}

func TestAgentFirewallMalformedListAndExtensionStayAligned(t *testing.T) {
	for _, denied := range [][]byte{fwFrame(11, 1), fwFrame(27), fwFrame(27, 1, 2), fwFrame(27, 255, 255, 255, 255), fwFrame(27, 0, 0, 0, 24, 's')} {
		binding := fwBinding()
		request := append(bytes.Clone(denied), fwFrame(11)...)
		list := fwFrame(12, 0, 0, 0, 0)
		_, _, output, upstream, err := fwMemoryRun(binding, request, append(fwFrame(6), list...))
		failure := byte(5)
		if denied[4] == 27 {
			failure = 28
		}
		if err != nil || !bytes.Equal(output, append(fwFrame(failure), list...)) || !bytes.Equal(upstream, append(bytes.Clone(binding.wire), fwFrame(11)...)) {
			t.Fatalf("malformed request reached source or consumed the next packet: err=%v", err)
		}
	}
}

func TestAgentFirewallRejectsBadRemoteFramingPerStream(t *testing.T) {
	for _, request := range [][]byte{{0, 0, 0}, {0, 0, 0, 0}, binary.BigEndian.AppendUint32(nil, 262145), {0, 0, 0, 1}} {
		binding := fwBinding()
		_, _, _, upstream, err := fwMemoryRun(binding, request, fwFrame(6))
		if err == nil || isAgentSourceFailure(err) || !bytes.Equal(upstream, binding.wire) {
			t.Fatalf("remote framing failure was forwarded or misclassified: err=%v", err)
		}
	}
}

func TestAgentFirewallRejectsUnexpectedOrTruncatedSourceReplies(t *testing.T) {
	for _, request := range []byte{11, 13, 27} {
		for _, reply := range [][]byte{fwFrame(99), fwFrame(5, 1), fwFrame(12), fwFrame(14), {0, 0, 0}, {0, 0, 0, 0}, binary.BigEndian.AppendUint32(nil, 262145), {0, 0, 0, 8, 14, 0}} {
			binding := fwBinding()
			wire := fwFrame(request)
			if request == 27 {
				wire = fwBindFrame(agentFWSessionBind, "key", "sid", "sig", 0)
			}
			_, _, _, _, err := fwMemoryRun(binding, wire, append(fwFrame(6), reply...))
			if !isAgentSourceFailure(err) {
				t.Fatalf("request %d accepted malformed source response: %v", request, err)
			}
		}
	}
}

func TestAgentFirewallSourceAndRemoteWriteErrorsAreDistinct(t *testing.T) {
	failed := errors.New("controlled write failure")
	for _, sourceFails := range []bool{false, true} {
		binding := fwBinding()
		remote := &fwMemoryConn{reader: bytes.NewReader(fwFrame(11)), writer: io.Discard}
		source := &fwMemoryConn{reader: bytes.NewReader(append(fwFrame(6), fwFrame(12, 0, 0, 0, 0)...)), writer: io.Discard}
		bad := fwWriterFunc(func([]byte) (int, error) { return 0, failed })
		if sourceFails {
			source.writer = bad
		} else {
			remote.writer = bad
		}
		err := forwardAgentAuthentication(context.Background(), remote, source, binding)
		if !errors.Is(err, failed) || isAgentSourceFailure(err) != sourceFails {
			t.Fatalf("write failure classification source=%v: %v", sourceFails, err)
		}
	}
}

func TestAgentFirewallResponseOpcodeMatrix(t *testing.T) {
	for _, request := range []byte{11, 13, 27} {
		for _, response := range []byte{5, 6, 12, 14, 28, 99} {
			binding := fwBinding()
			wire := fwFrame(request)
			if request == 27 {
				wire = fwBindFrame(agentFWSessionBind, "key", "sid", "sig", 0)
			}
			reply := fwFrame(response)
			if response == 12 {
				reply = fwFrame(12, 0, 0, 0, 0)
			}
			if response == 14 {
				reply = fwFrame(14, fwString("signature")...)
			}
			allowed := response == 5 || request == 11 && response == 12 || request == 13 && response == 14 || request == 27 && (response == 6 || response == 28)
			_, _, output, _, err := fwMemoryRun(binding, wire, append(fwFrame(6), reply...))
			if allowed {
				if err != nil || !bytes.Equal(output, reply) {
					t.Fatalf("valid request/reply %d/%d: %v", request, response, err)
				}
			} else if !isAgentSourceFailure(err) || len(output) != 0 {
				t.Fatalf("invalid request/reply %d/%d escaped source classification: %v", request, response, err)
			}
		}
	}
}

func TestAgentFirewallPreCancelledContextDoesNotWriteAPrefix(t *testing.T) {
	var downstream, upstream bytes.Buffer
	remote := &fwMemoryConn{reader: bytes.NewReader(fwFrame(11)), writer: &downstream}
	source := &fwMemoryConn{reader: bytes.NewReader(fwFrame(6)), writer: &upstream}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := forwardAgentAuthentication(ctx, remote, source, fwBinding()); !errors.Is(err, context.Canceled) || upstream.Len() != 0 || downstream.Len() != 0 {
		t.Fatalf("cancelled firewall wrote a binding or data operation: %v", err)
	}
	if !remote.closed || !source.closed {
		t.Fatal("cancelled firewall retained an owned endpoint")
	}
}

func TestAgentFirewallIOCompletesPartialWritesWithoutAcceptingNoProgress(t *testing.T) {
	want := fwFrame(13, []byte("opaque request")...)
	var got bytes.Buffer
	calls := 0
	if err := writeAgentFirewallBytes(fwWriterFunc(func(p []byte) (int, error) {
		calls++
		if calls > len(want)+1 {
			return 0, errors.New("partial loop made no progress")
		}
		return got.Write(p[:min(3, len(p))])
	}), want); err != nil || !bytes.Equal(got.Bytes(), want) || calls < 2 {
		t.Fatalf("partial writer changed the packet: calls=%d err=%v", calls, err)
	}
	failed := errors.New("controlled firewall write failure")
	for _, outcome := range []struct {
		n   int
		err error
	}{{0, nil}, {1000, nil}, {2, failed}, {0, failed}} {
		calls := 0
		err := writeAgentFirewallBytes(fwWriterFunc(func([]byte) (int, error) {
			calls++
			if calls > 1 {
				return 0, failed
			}
			return outcome.n, outcome.err
		}), want)
		expected := outcome.err
		if expected == nil {
			expected = io.ErrShortWrite
		}
		if !errors.Is(err, expected) || calls != 1 {
			t.Fatalf("invalid writer result was retried or accepted: calls=%d err=%v", calls, err)
		}
	}
}

type fwCloseBarrier struct {
	mu        sync.Mutex
	deadlines [2]bool
	ready     chan struct{}
	once      sync.Once
}

type fwCloseConn struct {
	net.Conn
	barrier *fwCloseBarrier
	side    int
}

func (c fwCloseConn) SetDeadline(time.Time) error {
	c.barrier.mu.Lock()
	c.barrier.deadlines[c.side] = true
	both := c.barrier.deadlines[0] && c.barrier.deadlines[1]
	c.barrier.mu.Unlock()
	if both {
		c.barrier.once.Do(func() { close(c.barrier.ready) })
	}
	return nil
}

func (c fwCloseConn) Close() error { <-c.barrier.ready; return nil }

func TestAgentFirewallExpiresBothDescriptorsBeforeAnyCloseCanWait(t *testing.T) {
	b := &fwCloseBarrier{ready: make(chan struct{})}
	remote, source := fwCloseConn{barrier: b, side: 0}, fwCloseConn{barrier: b, side: 1}
	done := make(chan error, 1)
	go func() { done <- forwardAgentAuthentication(context.Background(), remote, source, nil) }()
	select {
	case err := <-done:
		if !isAgentSourceFailure(err) {
			t.Fatalf("missing binding classification: %v", err)
		}
	case <-time.After(5 * time.Second):
		// Free a broken implementation before reporting the semantic failure.
		b.once.Do(func() { close(b.ready) })
		<-done
		t.Fatal("firewall waited in Close before interrupting both owned descriptors")
	}
}
