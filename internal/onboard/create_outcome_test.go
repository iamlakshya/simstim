package onboard

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type cleanupOpenFailure struct {
	fakeProvider
	cancel context.CancelFunc
	opens  int
}

func (p *cleanupOpenFailure) Open(client.Host) (client.Lifecycle, error) {
	p.opens++
	if p.opens == 1 {
		return p.m, nil
	}
	return nil, errors.New("the provider cannot open its new machine")
}

func (p *cleanupOpenFailure) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	p.cancel()
	return p.fakeProvider.Create(ctx, req)
}

func TestCancelledCreateReportsACleanupOpenFailure(t *testing.T) {
	base := newFake(t, Docker)
	base.block = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cleanupOpenFailure{fakeProvider: base, cancel: cancel}
	_, err := Create(ctx, p, provider.Request{Name: "box", Out: io.Discard})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "cancelled" || ue.Kind != client.KindCancelled ||
		!strings.Contains(ue.Msg, "may be left: the provider cannot open its new machine") ||
		strings.Contains(ue.Msg, "was deleted") || ue.Hint != "Delete it with docker's own tools." {
		t.Fatalf("failed cleanup open = %+v, want an accurate retained-machine report", err)
	}
	if p.opens != 2 || slices.Contains(p.m.Calls(), "destroy") {
		t.Fatalf("cleanup calls: opens %d, lifecycle %q", p.opens, p.m.Calls())
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("cancelled creation saved hosts: %+v, %v", hosts, err)
	}
	_, unlock, err := client.LockHost(t.Context(), "box", "after failed cleanup")
	if err != nil {
		t.Fatalf("the failed cleanup kept its host lock: %v", err)
	}
	unlock()
}

func TestUnsavedKeepsTheActualSaveFailureDuringALateCancel(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		failure    error
		kind       client.Kind
	}{
		{"busy", "hosts_busy", &client.UserError{Msg: "the actual save lock timed out", Hint: "End the other command.", Code: "hosts_busy"}, client.KindFailed},
		{"taken", "host_exists", &client.UserError{Msg: "another writer took this name", Hint: "Choose another name.", Code: "host_exists"}, client.KindFailed},
		{"deadline", "save_failed", context.DeadlineExceeded, client.KindFailed},
		{"typed deadline", "timeout", &client.StoppedError{UserError: &client.UserError{Msg: "the actual save deadline ended", Hint: "Run the command again.", Code: "timeout"}, Err: context.DeadlineExceeded}, client.KindFailed},
		{"disk", "save_failed", errors.New("the disk could not save"), client.KindFailed},
		{"cancel", "cancelled", context.Canceled, client.KindCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFake(t, "fake-outcome")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h := client.Host{Name: "box", Machine: &client.Machine{Provider: p.Name(), ID: "assigned-new-machine"}}
			err := unsaved(ctx, p, provider.Request{Name: h.Name, Out: io.Discard}, h, tc.failure)
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Code != tc.code || ue.Kind != tc.kind || !strings.Contains(ue.Msg, "was deleted again") {
				t.Fatalf("save failure %v with late cancel = %+v, want %s/%s and actual cleanup", tc.failure, err, tc.code, tc.kind)
			}
			var original *client.UserError
			if errors.As(tc.failure, &original) && (ue.Hint != original.Hint || !strings.HasPrefix(ue.Msg, original.Msg)) {
				t.Fatalf("late cancel replaced the actual save error: %+v", ue)
			}
			if !slices.Contains(p.m.Calls(), "destroy") {
				t.Fatal("the unsaved machine was not cleaned up")
			}
		})
	}
}
