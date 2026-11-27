package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type uploadPatternReader struct{}

func (uploadPatternReader) Read(out []byte) (int, error) {
	for index := range out {
		out[index] = 'P'
	}
	return len(out), nil
}

type uploadPatternSink struct{ received atomic.Int64 }

func (s *uploadPatternSink) Write(payload []byte) (int, error) {
	for _, value := range payload {
		if value != 'P' {
			return 0, fmt.Errorf("upstream upload byte changed")
		}
	}
	s.received.Add(int64(len(payload)))
	return len(payload), nil
}

func waitUploadHold(t *testing.T, server *Server) api.PendingNetworkRequest {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		for _, row := range server.pendingNetworkSnapshot().Requests {
			if row.Reason == api.NetworkHoldUploadBudget && row.Waiting > 0 {
				return row
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("actual upload never reached its live budget question")
		case <-tick.C:
		}
	}
}

func TestActualTwentyMiBUploadPausesAtSixteenAndResumesOrRefusesThroughOriginalHold(t *testing.T) {
	for _, answer := range []string{"once", "raise", "refuse", "expire", "source-revoke"} {
		t.Run(answer, func(t *testing.T) {
			const total = 20 << 20
			stops := answer == "refuse" || answer == "expire" || answer == "source-revoke"
			var sink uploadPatternSink
			prefix := make(chan struct{})
			tail := make(chan struct{})
			release := sync.OnceFunc(func() { close(tail) })
			defer release()
			originDone := make(chan error, 1)
			dialer := &Dialer{
				Lookup: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
				},
				DialTCP: func(context.Context, netip.AddrPort) (net.Conn, error) {
					client, origin := net.Pipe()
					t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
					go func() {
						defer origin.Close()
						if _, err := io.CopyN(&sink, origin, defaultUploadBudget); err != nil {
							originDone <- err
							return
						}
						close(prefix)
						select {
						case <-tail:
						case <-t.Context().Done():
							originDone <- t.Context().Err()
							return
						}
						_, err := io.CopyN(&sink, origin, total-defaultUploadBudget)
						originDone <- err
					}()
					return client, nil
				},
			}
			start := time.Now()
			var clock atomic.Pointer[time.Time]
			clock.Store(&start)
			env := startTestServerWithClock(t, dialer, func() time.Time { return *clock.Load() })
			policy := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "paste.example:8443"})
			env.call(t, CtlInit, InitArgs{SessionID: "budget234", Agent: "shell", Policy: policy})
			stream, err := env.supSess.Open(t.Context(), link.KindEgress, StreamHeader{Via: "explicit", Host: "paste.example", Port: 8443})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
			if _, err := io.WriteString(stream, "CONNECT paste.example:8443 HTTP/1.1\r\nHost: paste.example:8443\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(stream)
			for first := true; ; first = false {
				line, err := reader.ReadString('\n')
				if err != nil || first && !strings.HasPrefix(line, "HTTP/1.1 200 ") {
					t.Fatal("actual granted CONNECT did not open", err)
				}
				if line == "\r\n" {
					break
				}
			}
			guestDone := make(chan error, 1)
			go func() {
				_, err := io.CopyN(stream, uploadPatternReader{}, total)
				_ = stream.CloseWrite()
				guestDone <- err
			}()
			select {
			case <-prefix:
			case <-time.After(5 * time.Second):
				t.Fatal("actual upstream did not receive the first16MiB")
			}
			row := waitUploadHold(t, env.srv)
			descriptor, err := env.srv.describeNetworkHold(memberJSON(t, networkHoldDescribeArgs{Version: 1, ID: row.ID}))
			if err != nil || descriptor.Reason != api.NetworkHoldUploadBudget || !api.ValidNetworkHoldDescriptor(descriptor, row.ID) {
				t.Fatal("owned descriptor lost its actual private budget purpose", err)
			}
			for _, scope := range []api.NetworkHoldScope{api.NetworkHoldSession, api.NetworkHoldRepo, api.NetworkHoldProfile} {
				args := api.NetworkHoldActionArgs{Version: 1, Session: "budget234", Request: row.ID, Revision: row.Revision, Maximum: api.NetworkHoldPublicOnly, Scope: scope}
				env.srv.policyMu.Lock()
				resolution, err := env.srv.holdResolutionLocked(args)
				env.srv.policyMu.Unlock()
				var failure *proto.Error
				if !errors.As(err, &failure) || failure.Code != "upload_budget_scope" || resolution.Target != "" {
					t.Fatal("budget purpose escaped as a durable host-only allowance", err)
				}
			}
			if sink.received.Load() != defaultUploadBudget {
				t.Fatal("physical upstream crossed or stopped before the exact16MiB cap")
			}
			// Reapplying an ordinary host allowance cannot answer this purpose.
			env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: policy})
			after := waitUploadHold(t, env.srv)
			if after.ID != row.ID || sink.received.Load() != defaultUploadBudget {
				t.Fatal("ordinary network policy replaced or answered the budget hold")
			}
			row = after
			if answer == "once" {
				args := api.NetworkHoldActionArgs{Version: 1, Session: "budget234", Request: row.ID, Revision: row.Revision, Maximum: api.NetworkHoldPublicOnly, Scope: api.NetworkHoldOnce}
				env.call(t, CtlNetworkAnswer, args)
				// The answer is bound to the waiting real Permit. A new flow
				// cannot steal its byte continuation or any network authority.
				engine := env.srv.currentEngine()
				fresh := engine.NewPermit()
				if engine.evaluate(Request{Host: "paste.example", Port: 8443, uploadBudgetOnly: true}, fresh, evalBudgetReserve).Allow {
					t.Fatal("a new flow stole the paused flow's budget answer")
				}
				fresh.Close()
			} else if answer == "raise" {
				policy.Network.UploadBudget = new("20M")
				env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: policy})
			} else if answer == "expire" {
				later := start.Add(6 * time.Minute)
				clock.Store(&later)
				env.srv.wakeExpiry()
			} else if answer == "source-revoke" {
				before := env.srv.currentMembers()
				env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: onceTestPolicy(t, api.PresetLocked)})
				if env.srv.currentMembers() == before {
					t.Fatal("source revocation did not replace the actual current policy state")
				}
			} else {
				env.call(t, CtlNetworkRefuse, networkHoldRefuseArgs{ID: row.ID})
			}
			release()
			select {
			case err := <-originDone:
				if !stops && (err != nil || sink.received.Load() != total) || stops && (err == nil || sink.received.Load() != defaultUploadBudget) {
					t.Fatal("actual byte continuation/refusal changed its physical boundary", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("budget answer did not resume or physically close its origin")
			}
			select {
			case err := <-guestDone:
				if !stops && err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("budget answer retained the original guest writer")
			}
			env.waitAudits(t, 1) // actual flow finaliser follows physical write accounting
			if answer == "expire" {
				before := env.srv.currentEngine()
				args := api.NetworkHoldActionArgs{Version: 1, Session: "budget234", Request: row.ID, Revision: row.Revision, Scope: api.NetworkHoldOnce, Maximum: api.NetworkHoldPublicOnly}
				if _, err := env.srv.answerNetworkHold(memberJSON(t, args)); err == nil || env.srv.currentEngine() != before {
					t.Fatal("expired original request minted a new byte continuation")
				}
			}
			env.srv.uploads.mu.Lock()
			used := env.srv.uploads.hosts["paste.example"].used
			env.srv.uploads.mu.Unlock()
			if used != sink.received.Load() {
				t.Fatal("budget counted buffered/read-ahead bytes instead of successful writes")
			}
		})
	}
}
