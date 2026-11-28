package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func TestReadHolderEventsWaitsForStartReplyBeforeForwardingEnded(t *testing.T) {
	var wire syncBuf
	w := proto.NewWriter(&wire)
	r := &runner{
		ctlW:   w,
		stopCh: make(chan struct{}),
	}
	evR, evW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = evW.WriteString(`{"type":"started","holder_pid":10,"agent_pid":20,"cols":80,"rows":24}` + "\n")
	_, _ = evW.WriteString(`{"type":"ended","exit_code":3}` + "\n")
	_ = evW.Close()

	started := make(chan host.GuestEvent, 1)
	startReplied := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.readHolderEvents(context.Background(), "abcd2345", evR, started, startReplied)
	}()

	select {
	case ev, ok := <-started:
		if !ok || ev.Type != "started" || ev.AgentPID != 20 {
			t.Fatalf("started event = %+v (ok=%v), want started with AgentPID 20", ev, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for started event")
	}

	// While startReplied is still open, ended must NOT be written onto ctl.
	time.Sleep(30 * time.Millisecond)
	if len(wire.Bytes()) != 0 {
		t.Fatalf("ctl received %d bytes before start reply was written", len(wire.Bytes()))
	}

	// Simulate handleCtlRequest writing the start TResponse, then releasing events.
	res, _ := json.Marshal(StartResp{HolderPID: 10, AgentPID: 20, Cols: 80, Rows: 24})
	if err := w.WriteJSON(proto.TResponse, proto.Response{ID: 1, OK: true, Result: res}); err != nil {
		t.Fatal(err)
	}
	close(startReplied)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readHolderEvents did not finish after pipe closed")
	}

	rd := bufio.NewReader(bytes.NewReader(wire.Bytes()))
	f1, err := proto.Read(rd)
	if err != nil || f1.Type != proto.TResponse {
		t.Fatalf("first frame = %v (%v), want TResponse", f1, err)
	}
	f2, err := proto.Read(rd)
	if err != nil || f2.Type != proto.TEvent {
		t.Fatalf("second frame = %v (%v), want TEvent", f2, err)
	}
	var ev Event
	if err := json.Unmarshal(f2.Payload, &ev); err != nil || ev.Type != "ended" || ev.ExitCode != 3 {
		t.Fatalf("second frame event = %+v (%v), want ended exit 3", ev, err)
	}
}

func TestReadHolderEventsDropsEventsOnStartFailed(t *testing.T) {
	var wire syncBuf
	r := &runner{
		ctlW:   proto.NewWriter(&wire),
		stopCh: make(chan struct{}),
	}
	evR, evW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = evW.WriteString(`{"type":"start_failed","reason":""}` + "\n")
	_, _ = evW.WriteString(`{"type":"ended","exit_code":1}` + "\n")
	_ = evW.Close()

	started := make(chan host.GuestEvent, 1)
	startReplied := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.readHolderEvents(context.Background(), "abcd2345", evR, started, startReplied)
	}()

	var ev host.GuestEvent
	select {
	case got, ok := <-started:
		if !ok {
			t.Fatal("started channel closed without start_failed event")
		}
		ev = got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for start_failed event")
	}
	close(startReplied)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readHolderEvents did not finish after start_failed")
	}
	if ev.Type != "start_failed" {
		t.Fatalf("event = %+v, want start_failed", ev)
	}
	if len(wire.Bytes()) != 0 {
		t.Fatalf("unexpected ctl frames after start_failed: %d bytes", len(wire.Bytes()))
	}
}
