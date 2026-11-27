package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fwReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > 262144 {
		return nil, errors.New("invalid fixture frame size")
	}
	body := make([]byte, int(n))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return append(header[:], body...), nil
}

func fwSendFrame(w io.Writer, frame []byte) error {
	n, err := w.Write(frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return io.ErrShortWrite
	}
	return nil
}

type fwPipeRun struct {
	host         net.Conn
	cancel       context.CancelFunc
	result       <-chan error
	sourceResult <-chan error
	remoteWrites <-chan struct{}
	sourceWrites <-chan struct{}
}

type fwWriteObserver struct {
	net.Conn
	writes chan struct{}
}

func (c fwWriteObserver) Write(p []byte) (int, error) {
	select {
	case c.writes <- struct{}{}:
	default:
	}
	return c.Conn.Write(p)
}

func fwPipes(t *testing.T, binding *peerAgentBinding, serve func(context.Context, net.Conn) error) *fwPipeRun {
	t.Helper()
	remote, host := net.Pipe()
	source, agent := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	result, sourceResult := make(chan error, 1), make(chan error, 1)
	remoteWrites := make(chan struct{}, 64)
	sourceWrites := make(chan struct{}, 64)
	finished := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		result <- forwardAgentAuthentication(ctx, fwWriteObserver{remote, remoteWrites}, fwWriteObserver{source, sourceWrites}, binding)
	}()
	go func() { defer workers.Done(); defer agent.Close(); sourceResult <- serve(ctx, agent) }()
	go func() { workers.Wait(); close(finished) }()
	t.Cleanup(func() {
		cancel()
		for _, c := range []net.Conn{remote, host, source, agent} {
			_ = c.SetDeadline(time.Now())
			_ = c.Close()
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("firewall retained owned I/O after cancellation")
		}
	})
	return &fwPipeRun{host: host, cancel: cancel, result: result, sourceResult: sourceResult, remoteWrites: remoteWrites, sourceWrites: sourceWrites}
}

func fwExpectPrefix(conn net.Conn, binding *peerAgentBinding) error {
	frame, err := fwReadFrame(conn)
	if err != nil {
		return err
	}
	if !bytes.Equal(frame, binding.wire) {
		return errors.New("source did not receive the exact outer prefix first")
	}
	return fwSendFrame(conn, fwFrame(6))
}

func fwResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("firewall did not finish its transaction")
		return nil
	}
}

func TestAgentFirewallTruncatedAllowedBodyCannotPerformAFramedOperation(t *testing.T) {
	binding := fwBinding()
	var completed atomic.Int32
	run := fwPipes(t, binding, func(_ context.Context, conn net.Conn) error {
		if err := fwExpectPrefix(conn, binding); err != nil {
			return err
		}
		if _, err := fwReadFrame(conn); err == nil {
			completed.Add(1)
		}
		return nil
	})
	// One full streaming chunk can reach the agent, but the declared packet
	// is incomplete. A properly framed source cannot dispatch its opcode.
	partial := append(binary.BigEndian.AppendUint32(nil, 65537), byte(13))
	partial = append(partial, bytes.Repeat([]byte{7}, 32768+2)...)
	if err := fwSendFrame(run.host, partial); err != nil {
		t.Fatal(err)
	}
	_ = run.host.Close()
	if err := fwResult(t, run.result); err == nil || isAgentSourceFailure(err) {
		t.Fatalf("truncated remote body classification: %v", err)
	}
	if err := fwResult(t, run.sourceResult); err != nil {
		t.Fatal(err)
	}
	if completed.Load() != 0 {
		t.Fatal("source dispatched an incomplete allowed packet")
	}
}

func TestAgentFirewallFragmentedPipelinedMessagesStayInExactOrder(t *testing.T) {
	binding := fwBinding()
	bind := fwBindFrame(agentFWSessionBind, "destination", "sid", "sig", 0)
	sign := fwFrame(13, []byte("opaque authentication data")...)
	denied := fwFrame(17, []byte("never add a key")...)
	list := fwFrame(11)
	signReply := fwFrame(14, fwString("first signature")...)
	listReply := fwFrame(12, 0, 0, 0, 0)
	run := fwPipes(t, binding, func(_ context.Context, conn net.Conn) error {
		if err := fwExpectPrefix(conn, binding); err != nil {
			return err
		}
		for i, want := range [][]byte{bind, sign, list} {
			got, err := fwReadFrame(conn)
			if err != nil || !bytes.Equal(got, want) {
				return fmt.Errorf("source request %d did not match the allowed sequence: %v", i, err)
			}
			reply := fwFrame(6)
			if i == 1 {
				reply = signReply
			} else if i == 2 {
				reply = listReply
			}
			// Fragment every header, opcode and payload boundary.
			for _, b := range reply {
				if err := fwSendFrame(conn, []byte{b}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	writeDone := make(chan error, 1)
	go func() {
		for _, frame := range [][]byte{bind, sign, denied, list} {
			for _, b := range frame {
				if err := fwSendFrame(run.host, []byte{b}); err != nil {
					writeDone <- err
					return
				}
			}
		}
		writeDone <- nil
	}()
	for i, want := range [][]byte{fwFrame(6), signReply, fwFrame(5), listReply} {
		got, err := fwReadFrame(run.host)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("remote response %d was out of order or fragmented: %v", i, err)
		}
	}
	if err := fwResult(t, writeDone); err != nil {
		t.Fatal(err)
	}
	_ = run.host.Close()
	if err := fwResult(t, run.result); err != nil {
		t.Fatal(err)
	}
	if err := fwResult(t, run.sourceResult); err != nil {
		t.Fatal(err)
	}
}

func TestAgentFirewallCancellationInterruptsPrefixAndSignReads(t *testing.T) {
	for _, phase := range []string{"prefix", "sign"} {
		t.Run(phase, func(t *testing.T) {
			binding := fwBinding()
			waiting := make(chan struct{})
			run := fwPipes(t, binding, func(_ context.Context, conn net.Conn) error {
				frame, err := fwReadFrame(conn)
				if err != nil || !bytes.Equal(frame, binding.wire) {
					return errors.New("missing prefix")
				}
				if phase == "sign" {
					if err := fwSendFrame(conn, fwFrame(6)); err != nil {
						return err
					}
					frame, err = fwReadFrame(conn)
					if err != nil || frame[4] != 13 {
						return errors.New("missing Sign request")
					}
				}
				close(waiting)
				var b [1]byte
				_, err = conn.Read(b[:]) // Close must interrupt this owned source too.
				return err
			})
			writeDone := make(chan error, 1)
			go func() { writeDone <- fwSendFrame(run.host, fwFrame(13, []byte("opaque")...)) }()
			select {
			case <-waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("source did not reach controlled wait")
			}
			run.cancel()
			if err := fwResult(t, run.result); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled %s wait: %v", phase, err)
			}
			if err := fwResult(t, run.sourceResult); err == nil {
				t.Fatal("source descriptor remained open after cancellation")
			}
			_ = fwResult(t, writeDone) // Prefix cancellation may stop the not-yet-read remote request.
		})
	}
}

func TestAgentFirewallCancellationInterruptsEveryOwnedWriteDirection(t *testing.T) {
	for _, phase := range []string{"prefix", "request", "response"} {
		t.Run(phase, func(t *testing.T) {
			binding := fwBinding()
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			run := fwPipes(t, binding, func(_ context.Context, conn net.Conn) error {
				if phase == "prefix" {
					<-gate
					return nil
				}
				if err := fwExpectPrefix(conn, binding); err != nil {
					return err
				}
				if phase == "request" {
					var head [5]byte
					if _, err := io.ReadFull(conn, head[:]); err != nil {
						return err
					}
					if head[4] != 13 {
						return errors.New("unexpected request opcode")
					}
					<-gate
					return nil
				}
				if _, err := fwReadFrame(conn); err != nil {
					return err
				}
				return fwSendFrame(conn, fwFrame(12, 0, 0, 0, 0))
			})
			t.Cleanup(release)
			request := fwFrame(11)
			if phase == "request" {
				request = fwFrame(13, []byte("opaque body")...)
			}
			writeDone := make(chan error, 1)
			go func() { writeDone <- fwSendFrame(run.host, request) }()
			writes, n := run.sourceWrites, 1
			if phase == "request" {
				n = 3
			} // Prefix, request header, then blocked body.
			if phase == "response" {
				writes, n = run.remoteWrites, 1
			}
			for range n {
				select {
				case <-writes:
				case <-time.After(5 * time.Second):
					t.Fatal("firewall did not reach its controlled write")
				}
			}
			run.cancel()
			if err := fwResult(t, run.result); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled %s write: %v", phase, err)
			}
			release()
			_ = fwResult(t, run.sourceResult)
			_ = fwResult(t, writeDone)
		})
	}
}

type fwPattern byte

func (p fwPattern) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(p)
	}
	return len(b), nil
}

func fwHash(h hash.Hash, parts ...io.Reader) []byte {
	for _, part := range parts {
		_, _ = io.Copy(h, part)
	}
	return h.Sum(nil)
}

func TestAgentFirewallStreamsMaximumBodiesWithOneFixedBuffer(t *testing.T) {
	binding := fwBinding()
	// A maximum-size shared prefix is retained once; it is not duplicated
	// into the per-stream buffer or parsed into key objects by the firewall.
	overhead := len(fwBindFrame(agentFWSessionBind, "k", "s", "g", 1)) - 4 - 1
	binding.wire = fwBindFrame(agentFWSessionBind, string(bytes.Repeat([]byte{'k'}, 262144-overhead)), "s", "g", 1)
	if binary.BigEndian.Uint32(binding.wire[:4]) != 262144 {
		t.Fatal("fixture prefix is not at the declared bound")
	}
	requestHead := append(binary.BigEndian.AppendUint32(nil, 262144), byte(13))
	replyHead := append(binary.BigEndian.AppendUint32(nil, 262144), byte(14))
	signatureLength := binary.BigEndian.AppendUint32(nil, 262139)
	request := io.MultiReader(bytes.NewReader(requestHead), io.LimitReader(fwPattern(7), 262143))
	reply := io.MultiReader(bytes.NewReader(fwFrame(6)), bytes.NewReader(replyHead), bytes.NewReader(signatureLength), io.LimitReader(fwPattern(9), 262139))
	toRemote, toSource := sha256.New(), sha256.New()
	remote := &fwMemoryConn{reader: request, writer: toRemote}
	source := &fwMemoryConn{reader: reply, writer: toSource}
	if err := forwardAgentAuthentication(context.Background(), remote, source, binding); err != nil {
		t.Fatal(err)
	}
	wantSource := fwHash(sha256.New(), bytes.NewReader(binding.wire), bytes.NewReader(requestHead), io.LimitReader(fwPattern(7), 262143))
	wantRemote := fwHash(sha256.New(), bytes.NewReader(replyHead), bytes.NewReader(signatureLength), io.LimitReader(fwPattern(9), 262139))
	if !bytes.Equal(toSource.Sum(nil), wantSource) || !bytes.Equal(toRemote.Sum(nil), wantRemote) {
		t.Fatal("streaming changed maximum-size request or response bytes")
	}
	union := map[*byte]bool{}
	for _, c := range []*fwMemoryConn{remote, source} {
		if c.maxRead > 32768 || c.maxWrite > 32768 {
			t.Fatalf("firewall used an oversized transfer: read=%d write=%d", c.maxRead, c.maxWrite)
		}
		if c.deadlines != 0 || c.closes == 0 {
			t.Fatal("a successful transaction imposed a per-operation deadline or retained a descriptor")
		}
		for p := range c.bodyBuffers {
			union[p] = true
		}
	}
	if len(union) != 1 {
		t.Fatalf("request and response used %d streaming buffers, want one", len(union))
	}
}

func TestAgentFirewallDoesNotTimeOutPrefixOrSigningConfirmation(t *testing.T) {
	type pending struct {
		phase     string
		run       *fwPipeRun
		release   chan struct{}
		responses <-chan error
	}
	var flows []pending
	for _, phase := range []string{"prefix", "sign"} {
		binding := fwBinding()
		waiting, release := make(chan struct{}), make(chan struct{})
		listReply := fwFrame(12, 0, 0, 0, 0)
		signReply := fwFrame(14, fwString("delayed signature")...)
		run := fwPipes(t, binding, func(ctx context.Context, conn net.Conn) error {
			if phase == "prefix" {
				got, err := fwReadFrame(conn)
				if err != nil || !bytes.Equal(got, binding.wire) {
					return errors.New("missing delayed prefix")
				}
			} else {
				if err := fwExpectPrefix(conn, binding); err != nil {
					return err
				}
				got, err := fwReadFrame(conn)
				if err != nil || got[4] != 13 {
					return errors.New("missing delayed Sign")
				}
			}
			close(waiting)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			if phase == "prefix" {
				if err := fwSendFrame(conn, fwFrame(6)); err != nil {
					return err
				}
			} else if err := fwSendFrame(conn, signReply); err != nil {
				return err
			}
			got, err := fwReadFrame(conn)
			if err != nil || !bytes.Equal(got, fwFrame(11)) {
				return errors.New("denied pipelined mutation reached the source or List was lost")
			}
			return fwSendFrame(conn, listReply)
		})
		request := fwFrame(11)
		want := [][]byte{listReply}
		if phase == "sign" {
			request = append(fwFrame(13, []byte("opaque authentication")...), fwFrame(17, []byte("denied mutation")...)...)
			request = append(request, fwFrame(11)...)
			want = [][]byte{signReply, fwFrame(5), listReply}
		}
		responses, finished := make(chan error, 1), make(chan struct{})
		var hostWorkers sync.WaitGroup
		hostWorkers.Add(2)
		writeDone := make(chan error, 1)
		go func() { defer hostWorkers.Done(); writeDone <- fwSendFrame(run.host, request) }()
		go func() {
			defer hostWorkers.Done()
			for _, expected := range want {
				got, err := fwReadFrame(run.host)
				if err != nil || !bytes.Equal(got, expected) {
					responses <- errors.New("delayed response order changed")
					return
				}
			}
			if err := <-writeDone; err != nil {
				responses <- err
				return
			}
			_ = run.host.Close()
			responses <- nil
		}()
		go func() { hostWorkers.Wait(); close(finished) }()
		t.Cleanup(func() {
			run.cancel()
			_ = run.host.Close()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("delayed host fixture did not stop")
			}
		})
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Fatal("source did not reach the confirmation wait")
		}
		flows = append(flows, pending{phase, run, release, responses})
	}
	// A global source confirmation can suppress even a binding response.
	// Use the real interval beyond the unrelated10s handshake bound. There
	// is no shortened signing timeout or fake-clock substitute in this proof.
	<-time.After(11 * time.Second)
	for _, flow := range flows {
		select {
		case err := <-flow.run.result:
			t.Fatalf("%s wait was timed out while its lease remained active: %v", flow.phase, err)
		default:
		}
		select {
		case <-flow.run.remoteWrites:
			t.Fatalf("%s wait emitted a reply before the pending source answer", flow.phase)
		default:
		}
	}
	for _, flow := range flows {
		close(flow.release)
	}
	for _, flow := range flows {
		if err := fwResult(t, flow.responses); err != nil {
			t.Fatal(err)
		}
		if err := fwResult(t, flow.run.result); err != nil {
			t.Fatal(err)
		}
		if err := fwResult(t, flow.run.sourceResult); err != nil {
			t.Fatal(err)
		}
	}
}
