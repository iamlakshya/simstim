package client

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// This observes the real per-application-write gate without a socket, TLS
// handshake, HTTP request, credential command or external network endpoint.
type localPurposeWireSpy struct{ writes, closes int }

func (s *localPurposeWireSpy) Read([]byte) (int, error) { return 0, io.EOF }
func (s *localPurposeWireSpy) Write(p []byte) (int, error) {
	s.writes++
	return len(p), nil
}
func (s *localPurposeWireSpy) Close() error                   { s.closes++; return nil }
func (*localPurposeWireSpy) LocalAddr() net.Addr              { return nil }
func (*localPurposeWireSpy) RemoteAddr() net.Addr             { return nil }
func (*localPurposeWireSpy) SetDeadline(time.Time) error      { return nil }
func (*localPurposeWireSpy) SetReadDeadline(time.Time) error  { return nil }
func (*localPurposeWireSpy) SetWriteDeadline(time.Time) error { return nil }

func TestForgeApplicationWriteRefusesRevokedSourceAndCancellation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "identical re-add"}[revoked], func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "policy", api.FeaturePolicyPush)
			request := NewPushGrantRequest()
			ctx := WithGrantRequest(t.Context(), request)
			t.Cleanup(func() {
				if err := request.Close(); err != nil {
					t.Error(err)
				}
			})
			intent, err := newPushGrantIntent(conn, api.PolicyAddArgs{ID: h5Policy().SessionID, Allow: true, Door: api.DoorPush, Target: "github.com/Owner/repo"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := bindRequestPushIntent(ctx, conn, intent); err != nil {
				t.Fatal(err)
			}
			if revoked {
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{h}); err != nil {
					t.Fatal(err)
				}
			} else {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			wire := &localPurposeWireSpy{}
			// No ACK capability is fabricated here. A revoked request must
			// fail before the write gate can even consult a value-bearing plan.
			plan := &localForgePlan{request: request, conn: conn, intent: intent}
			connection := &forgeHTTPConnection{Conn: wire, raw: wire, plan: plan, ctx: ctx}
			n, err := connection.Write([]byte("must remain local"))
			if err == nil || n != 0 || wire.writes != 0 || wire.closes != 1 {
				t.Fatalf("refused write escaped its gate: n=%d writes=%d closes=%d err=%v", n, wire.writes, wire.closes, err)
			}
			if !revoked && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost its original error", err)
			}
		})
	}
}

func TestRootRevokedSourceRefusesBeforeProbeOrNativePhase(t *testing.T) {
	h := h5GrantState(t)
	conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "sandbox")
	request := NewRootStepGrantRequest()
	ctx := WithGrantRequest(t.Context(), request)
	t.Cleanup(func() {
		if err := request.Close(); err != nil {
			t.Error(err)
		}
	})
	script, err := BuildRootScript("fixture", 1000, []RootStepID{RootLinger}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Private test data can name the original script, but cannot renew the
	// actual checked transport Source or cause even a probe after revocation.
	intent := &rootStepIntent{conn: conn, script: script, mode: RootRun, yes: true}
	request.root = intent
	execution := &rootStepExecution{intent: intent, request: request}
	request.rootExecution = execution
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.start(ctx, rootStepPasswordlessProbe, RootRunOptions{Passwordless: true}); err == nil {
		t.Fatal("a raw private script renewed the revoked transport Source")
	}
	if conn.nextID.Load() != 0 || execution.attempted || execution.probeStarted || execution.process != nil {
		t.Fatal("revocation reached a protocol probe or native phase")
	}
}

func TestBusyLocalPurposeCloseRevokesAdmissionAndAllowsCleanupRetry(t *testing.T) {
	request := NewPushGrantRequest()
	ctx := WithGrantRequest(t.Context(), request)
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		t.Fatal(err)
	}
	plan := &localForgePlan{request: request, busy: true}
	request.forge = plan
	if err := request.Close(); err == nil {
		t.Fatal("busy resource ownership was discarded")
	}
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err == nil {
		t.Fatal("incomplete cleanup renewed logical request admission")
	}
	plan.mu.Lock()
	plan.busy = false
	plan.mu.Unlock()
	if err := request.Close(); err != nil {
		t.Fatal("joined resource ownership could not finish cleanup", err)
	}
}
