package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

func encodedVTFrame(t *testing.T, kind byte, payload []byte) []byte {
	t.Helper()
	f, err := vtFrame(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestVTFramePreservesTheWireBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("日本語\x00\x1b[31m"), 100)
	f, err := vtFrame(vtFrameData, payload)
	if err != nil || f[0] != vtFrameData || binary.LittleEndian.Uint32(f[1:5]) != uint32(len(payload)) || !bytes.Equal(f[5:], payload) {
		t.Fatalf("frame changed its kind, length or payload: %v", err)
	}
}

func TestVTFrameSizeRejectsOverflowBeforeAllocation(t *testing.T) {
	for _, n := range []int{-1, math.MaxInt - 4, math.MaxInt} {
		if _, err := vtFrameSize(n); err == nil {
			t.Fatalf("frame size accepted payload length %d", n)
		}
	}
	if strconv.IntSize == 64 {
		n := uint64(math.MaxUint32) + 1
		if _, err := vtFrameSize(int(n)); err == nil {
			t.Fatal("frame size accepted a truncated wire length")
		}
	}
	maxPayload := min(uint64(math.MaxUint32), uint64(math.MaxInt)-5)
	size, err := vtFrameSize(int(maxPayload))
	if err != nil || uint64(size) != maxPayload+5 {
		t.Fatalf("largest representable frame size = %d, %v", size, err)
	}
}

func TestVTRejectedStartupJoinsChildAndWaiters(t *testing.T) {
	path, _ := fakeVT(t)
	v := &VT{opt: VTOptions{Path: path}, done: make(chan struct{})}
	c, err := v.spawn(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.kill)
	waiter := make(chan vtReply, 1)
	v.mu.Lock()
	c.waiters[vtFrameSync] = []chan vtReply{waiter}
	v.mu.Unlock()
	joined := make(chan struct{})
	go func() {
		c.stopUnstarted()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("refused startup left a child or writer running")
	}
	if c.cmd.ProcessState == nil {
		t.Fatal("refused startup did not reap its child")
	}
	select {
	case reply := <-waiter:
		if !errors.Is(reply.err, ErrVTDown) {
			t.Fatalf("waiter received %v, want terminal failure", reply.err)
		}
	default:
		t.Fatal("refused startup left a waiter pending")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !c.dead || len(c.waiters) != 0 || v.child != nil {
		t.Fatal("refused startup retained live state")
	}
}
