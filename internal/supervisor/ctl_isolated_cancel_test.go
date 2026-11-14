package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestMemberStoreCallerCancellationKeepsOwnedControlAndDropsLateAck(t *testing.T) {
	local, remote := net.Pipe()
	owner := link.New(local, link.Supervisor, link.Options{})
	guest := link.New(remote, link.Guest, link.Options{})
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	accepted := make(chan *link.Stream, 1)
	go func() { stream, _ := guest.Accept(ctx); accepted <- stream }()
	stream, err := owner.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	if peer == nil {
		t.Fatal("owned ctl was not accepted")
	}
	s := &Supervisor{lifetime: ctx, link: owner}
	control := newCtl(s, stream)
	t.Cleanup(func() { _ = owner.Close(); _ = guest.Close(); <-control.done })
	reader, writer := bufio.NewReader(peer), proto.NewWriter(peer)
	read := func() proto.Request {
		frame, err := proto.ReadLimit(reader, maxEvent)
		if err != nil {
			t.Fatal(err)
		}
		var request proto.Request
		if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil {
			t.Fatal("invalid owned control request")
		}
		return request
	}
	call, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- control.requestDetached(call, proto.CtlMemberStore, struct{}{}, nil) }()
	first := read()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("member phase cancellation did not return", err)
	}
	s.mu.Lock()
	lost := s.lost
	s.mu.Unlock()
	select {
	case <-owner.Done():
		t.Fatal("member caller cancellation killed sandbox link")
	default:
	}
	if lost != "" {
		t.Fatal("member caller cancellation became sandbox-fatal")
	}
	if err := writer.WriteJSON(proto.TResponse, proto.Response{ID: first.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	var features proto.CtlGuestFeatures
	go func() { result <- control.requestDetached(ctx, proto.CtlGuestCapabilities, struct{}{}, &features) }()
	next := read()
	if next.ID == first.ID || next.Op != proto.CtlGuestCapabilities {
		t.Fatal("late Store ACK selected another operation")
	}
	body, _ := json.Marshal(proto.CtlGuestFeatures{Version: 1, Features: []string{proto.GuestMemberPrepareV1}})
	if err := writer.WriteJSON(proto.TResponse, proto.Response{ID: next.ID, OK: true, Result: body}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil || features.Version != 1 {
		t.Fatal("owned control could not serve after isolated cancellation", err)
	}
}
