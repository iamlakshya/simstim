package guestpty

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func testChannels(t *testing.T) (*Channel, *Channel) {
	t.Helper()
	a, b := net.Pipe()
	init, err := New(a, Init, "abcdefgh", "fresh-run")
	if err != nil {
		t.Fatal(err)
	}
	holder, err := New(b, Holder, "abcdefgh", "fresh-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { init.Close(); holder.Close() })
	return init, holder
}

func TestPTYByteExactAndTerminalAfterFinalDrain(t *testing.T) {
	init, holder := testChannels(t)
	terminal, err := NewTerminal(holder)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	payload := append(bytes.Repeat([]byte{0, 0x1b, '[', '?', '2', '0', '0', '4', 'h', 0xff}, MaxData/4), []byte("last-byte\n")...)
	sent := make(chan error, 1)
	go func() {
		if err := init.Output(payload); err != nil {
			sent <- err
			return
		}
		sent <- init.End(proto.Ended{ExitCode: 37})
	}()
	finished := make(chan proto.Ended, 1)
	go func() {
		result, err := terminal.Wait()
		if err != nil {
			result.ExitCode = -1
		}
		finished <- result
	}()
	select {
	case <-finished:
		t.Fatal("terminal result bypassed unread output")
	default:
	}
	actual, err := io.ReadAll(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, payload) {
		t.Fatal("holder changed raw PTY bytes")
	}
	if result := <-finished; result.ExitCode != 37 {
		t.Fatal("terminal result lost", result)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := init.Output([]byte("late")); err == nil {
		t.Fatal("output followed terminal result")
	}
}

func TestPTYRejectsStaleRunAndWrongDirection(t *testing.T) {
	for _, test := range []struct {
		name, token string
		kind        proto.Type
	}{{"stale", "previous-run", proto.TInput}, {"direction", "fresh-run", proto.TOutput}} {
		t.Run(test.name, func(t *testing.T) {
			init, holder := testChannels(t)
			sender, err := New(holder.conn, Holder, "abcdefgh", test.token)
			if err != nil {
				t.Fatal(err)
			}
			sent := make(chan error, 1)
			go func() { sent <- sender.writer.Write(test.kind, sender.prefix([]byte("unsafe"))) }()
			if _, err := init.Read(); err == nil {
				t.Fatal("stale or reversed capability accepted")
			}
			<-sent
		})
	}
}

func TestPTYLengthRefusedBeforeWaitingForBody(t *testing.T) {
	init, holder := testChannels(t)
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], proto.MaxFrame)
	go func() { _, _ = holder.conn.Write(header[:]) }()
	result := make(chan error, 1)
	go func() { _, err := init.Read(); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("oversized frame accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("untrusted body was awaited before channel bound")
	}
}

func TestPTYControlsHaveClosedTypesAndSignals(t *testing.T) {
	init, holder := testChannels(t)
	for _, number := range []uint32{0, 4, 6, 10, 18, 19, 20, 31, 0xffffffff} {
		if holder.Signal(number) == nil {
			t.Fatal("unallowed signal encoded", number)
		}
	}
	var size proto.Resize
	for _, body := range []string{`{"cols":80,"rows":24,"pid":1}`, `{"cols":80} {"rows":24}`, `{"cols":80,"Cols":120,"rows":24}`, `{"cols":80,"cols":120,"rows":24}`, `null`} {
		if DecodeControl([]byte(body), &size) == nil {
			t.Fatal("open control accepted")
		}
	}
	sent := make(chan error, 1)
	go func() { sent <- holder.Resize(proto.Resize{Cols: 120, Rows: 40}) }()
	frame, err := init.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeControl(frame.Payload, &size); err != nil || size.Cols != 120 || size.Rows != 40 {
		t.Fatal("resize scope/body lost", err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestPTYCloseReleasesBlockedOutputAndResult(t *testing.T) {
	init, holder := testChannels(t)
	terminal, err := NewTerminal(holder)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- init.Output(bytes.Repeat([]byte("x"), 2*MaxData)) }()
	terminal.Close()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("EOF retained an output writer")
	}
	done := make(chan struct{})
	go func() { _, _ = terminal.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed terminal retained its result goroutine")
	}
}
