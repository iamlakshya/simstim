//go:build linux

package guest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type blockedVMReply struct {
	began, release chan struct{}
	once           sync.Once
}

func (w *blockedVMReply) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.began) })
	<-w.release
	return len(data), nil
}

func TestVMControlEffectsReceiptFinishesBeforeBlockedReplyIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vm := &vmRuntime{ctx: ctx, cancel: cancel}
	guest := &Init{vm: vm}
	output := &blockedVMReply{began: make(chan struct{}), release: make(chan struct{})}
	complete := make(chan struct{})
	go func() {
		defer close(complete)
		guest.request(proto.NewWriter(output), proto.Request{ID: 1, Op: "unknown-test-op"})
	}()
	select {
	case <-output.began:
	case <-time.After(time.Second):
		t.Fatal("request never reached its response")
	}
	joined := make(chan struct{})
	go func() { vm.controlWG.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("blocked response retained a filesystem operation")
	}
	close(output.release)
	<-complete
}

func TestVMControlCancellationRefusesNewEffectsAndJoinsExistingReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	vm := &vmRuntime{ctx: ctx, cancel: cancel}
	release, err := vm.controlOperation()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := vm.controlOperation(); err == nil {
		t.Fatal("control effects began after VM cancellation")
	}
	joined := make(chan struct{})
	go func() { vm.controlWG.Wait(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("in-flight file operation was not joined")
	default:
	}
	release()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("completed control receipt leaked")
	}
}
