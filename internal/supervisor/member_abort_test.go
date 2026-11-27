package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestFailedOrCancelledPreparationSendsExactCleanupBeforeReturning(t *testing.T) {
	for _, scenario := range []string{"negative-prepare", "cancelled-prepare"} {
		t.Run(scenario, func(t *testing.T) {
			lifetime, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			local, remote := net.Pipe()
			owner := link.New(local, link.Supervisor, link.Options{})
			guest := link.New(remote, link.Guest, link.Options{})
			accepted := make(chan *link.Stream, 1)
			go func() { stream, _ := guest.Accept(lifetime); accepted <- stream }()
			stream, err := owner.Open(lifetime, link.KindCtl, nil)
			if err != nil {
				t.Fatal(err)
			}
			peer := <-accepted
			if peer == nil {
				t.Fatal("owned control unavailable")
			}
			s := &Supervisor{id: "parent234", m: &host.Manifest{ID: "parent234"}, lifetime: lifetime, link: owner, rec: record{phase: host.LifeRunning}}
			s.ctl = newCtl(s, stream)
			t.Cleanup(func() { _ = owner.Close(); _ = guest.Close(); <-s.ctl.done })
			parent, cancel := context.WithCancel(lifetime)
			defer cancel()
			budget := newActiveBootBudget(parent, 30*time.Second)
			defer budget.close()
			args := proto.CtlStartArgs{Session: "child234", RunToken: "child-run", Argv: []string{"/bin/true"}, Env: []string{"HOME=/home/agent"}, Cwd: "/work", SecretModes: map[string]api.SecretMode{}}
			result := make(chan error, 1)
			go func() { result <- s.prepareMemberPhases(budget, &args, proto.CtlSpawn, true) }()
			reader, writer := bufio.NewReader(peer), proto.NewWriter(peer)
			read := func(op string) proto.Request {
				frame, err := proto.ReadLimit(reader, maxEvent)
				if err != nil {
					t.Fatal(err)
				}
				var request proto.Request
				if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil || request.Op != op {
					t.Fatal("failed preparation issued Store or final launch")
				}
				return request
			}
			reply := func(request proto.Request, value any, failure *proto.Error) {
				raw, _ := json.Marshal(value)
				if err := writer.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, OK: failure == nil, Result: raw, Error: failure}); err != nil {
					t.Fatal(err)
				}
			}
			capability := read(proto.CtlGuestCapabilities)
			reply(capability, proto.CtlGuestFeatures{Version: 1, Features: []string{proto.GuestMemberPrepareV1}}, nil)
			preparation := read(proto.CtlMemberPrepare)
			if scenario == "negative-prepare" {
				reply(preparation, nil, &proto.Error{Code: "test_prepare", Message: "original preparation failure", Hint: "fixture"})
			} else {
				cancel()
			}
			cleanup := read(proto.CtlMemberAbort)
			var original proto.CtlMemberPrepareArgs
			if json.Unmarshal(cleanup.Args, &original) != nil || original.Target != proto.CtlSpawn || original.Start.Session != args.Session || original.Start.RunToken != args.RunToken || original.Start.PreparedClaim != "" {
				t.Fatal("lost preparation ACK used an invented claim or wrong cleanup tuple")
			}
			reply(cleanup, proto.CtlMemberAbortResult{Version: 1, State: "aborted"}, nil)
			err = <-result
			if err == nil || scenario == "cancelled-prepare" && !errors.Is(err, context.Canceled) || scenario == "negative-prepare" && !strings.Contains(err.Error(), "original preparation failure") {
				t.Fatal("cleanup lost original phase failure", err)
			}
			select {
			case <-owner.Done():
				t.Fatal("phase cleanup killed the live control link")
			default:
			}
		})
	}
}
func TestControlUnsentFactCannotComeFromRemoteErrorCode(t *testing.T) {
	remote := claimedError(&proto.Error{Code: "ctl_unsent", Message: "claimed unsent", Hint: "claim"})
	if controlWasNotSent(remote) {
		t.Fatal("remote claims minted local transport state")
	}
	local := &ctlUnsentError{cause: context.Canceled}
	if !controlWasNotSent(local) || !errors.Is(local, context.Canceled) {
		t.Fatal("local unsent fact lost wrapped cancellation")
	}
	if err := answerOf(proto.CtlMemberStore, proto.Response{OK: false}, nil); err == nil {
		t.Fatal("unsuccessful response without an error became a Store ACK")
	}
}
