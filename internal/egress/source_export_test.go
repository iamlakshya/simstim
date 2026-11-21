package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type exportBlockedConn struct {
	net.Conn
	block     atomic.Bool
	partial   bool
	entered   chan struct{}
	returned  chan struct{}
	closed    chan struct{}
	enter     sync.Once
	end       sync.Once
	closeOnce sync.Once
}

func (conn *exportBlockedConn) Write(data []byte) (int, error) {
	if !conn.block.Load() {
		return conn.Conn.Write(data)
	}
	conn.enter.Do(func() { close(conn.entered) })
	n := 0
	if conn.partial && len(data) > 0 {
		var err error
		n, err = conn.Conn.Write(data[:1])
		if err != nil {
			return n, err
		}
	}
	<-conn.closed
	conn.end.Do(func() { close(conn.returned) })
	return n, net.ErrClosed
}
func (conn *exportBlockedConn) Close() error {
	conn.closeOnce.Do(func() { close(conn.closed) })
	return conn.Conn.Close()
}

func exportFixture(t *testing.T, partial bool, initial ...BoundInitArgs) (*Server, *link.Session, *link.Stream, *exportBlockedConn, api.NetworkHoldActionArgs) {
	t.Helper()
	a, b := holdTransportPair(t)
	owned := &exportBlockedConn{Conn: b, partial: partial, entered: make(chan struct{}), returned: make(chan struct{}), closed: make(chan struct{})}
	server, err := NewServer(owned, Options{Dialer: &Dialer{}})
	if err != nil {
		t.Fatal(err)
	}
	peer := link.New(a, link.Supervisor, LinkOptions())
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	if len(initial) == 0 {
		_, err = server.initialiseControl(memberJSON(t, InitArgs{SessionID: "export2", Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}}))
	} else {
		initial[0].Initial.SessionID = "export2"
		_, err = server.initialiseBoundControl(memberJSON(t, initial[0]))
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outgoing, err := peer.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := server.sess.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server.ctlMu.Lock()
	server.ctlStream = incoming
	server.ctl = proto.NewWriter(incoming)
	server.ctlMu.Unlock()
	server.policyMu.Lock()
	ticket, _, err := server.holds.attach(Request{Host: "authored.example", Port: 443}, "authored.example", server.now(), time.Minute)
	server.policyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return server, peer, outgoing, owned, api.NetworkHoldActionArgs{Version: 1, Session: "export2", Request: ticket.entry.meta.ID, Revision: ticket.entry.meta.Revision, Scope: api.NetworkHoldRepo, Maximum: api.NetworkHoldPublicOnly}
}

func readExport(t *testing.T, stream *link.Stream) (proto.Response, []byte) {
	t.Helper()
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := proto.Read(bufio.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var response proto.Response
	if json.Unmarshal(frame.Payload, &response) != nil {
		t.Fatal("invalid response")
	}
	return response, frame.Payload
}

func TestHoldResolutionChecksPrivacyAtTheOwnedWriterTurn(t *testing.T) {
	server, _, stream, _, args := exportFixture(t, false)
	server.ctlMu.Lock()
	finished := make(chan error, 1)
	go func() {
		finished <- server.writeNetworkHoldResolution(t.Context(), server.ctl, proto.Request{ID: 1, Args: memberJSON(t, args)})
	}()
	server.policyMu.Lock()
	stateArgs, err := server.currentMembers().initialArgs()
	if err != nil {
		t.Fatal(err)
	}
	stateArgs.Secrets["NEW"] = "authored.example"
	next, err := prepareInitialState(stateArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.retireJudgeSourcesLocked(t.Context(), next.state); err != nil {
		t.Fatal(err)
	}
	server.publishControlState(next, false)
	server.policyMu.Unlock()
	server.ctlMu.Unlock()
	response, raw := readExport(t, stream)
	if response.OK || response.Error == nil || response.Error.Code != "network_sensitive_scope" || strings.Contains(string(raw), "authored.example") {
		t.Fatal("queued resolution disclosed a newly sensitive target")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestHoldResolutionCancellationJoinsActualControlWriterBeforeSourceReplacement(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "partial"}[partial], func(t *testing.T) {
			server, _, _, owned, args := exportFixture(t, partial)
			owned.block.Store(true)
			finished := make(chan error, 1)
			go func() {
				finished <- server.writeNetworkHoldResolution(t.Context(), server.ctl, proto.Request{ID: 1, Args: memberJSON(t, args)})
			}()
			select {
			case <-owned.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual raw control write did not enter")
			}
			server.policyMu.Lock()
			before := server.currentMembers()
			nextArgs, err := before.initialArgs()
			if err != nil {
				t.Fatal(err)
			}
			nextArgs.Secrets["NEW"] = "authored.example"
			next, err := prepareInitialState(nextArgs)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := server.retireJudgeSourcesLocked(ctx, next.state); err != nil {
				t.Fatal(err)
			}
			select {
			case <-owned.returned:
			default:
				t.Fatal("source retirement returned before actual underlying writer joined")
			}
			if server.currentMembers() != before {
				t.Fatal("source installed during export cancellation")
			}
			server.publishControlState(next, false)
			server.policyMu.Unlock()
			if err := <-finished; err == nil {
				t.Fatal("cancelled partial export succeeded")
			}
			if server.sess.Err() == nil {
				t.Fatal("partial frame left the owned link alive")
			}
			server.flows.mu.Lock()
			owner := server.flows.export
			server.flows.mu.Unlock()
			if owner != nil {
				t.Fatal("completed cancelled export retained source ownership")
			}
		})
	}
}

func TestCompletedHoldPublicSnapshotSurvivesLaterSourceChangeWithoutPermission(t *testing.T) {
	server, _, stream, _, args := exportFixture(t, false)
	finished := make(chan error, 1)
	go func() {
		finished <- server.writeNetworkHoldResolution(t.Context(), server.ctl, proto.Request{ID: 1, Args: memberJSON(t, args)})
	}()
	response, _ := readExport(t, stream)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var result api.NetworkHoldResolution
	if !response.OK || json.Unmarshal(response.Result, &result) != nil || result.Target != "authored.example:443" {
		t.Fatal("current public snapshot did not complete")
	}
	server.policyMu.Lock()
	nextArgs, err := server.currentMembers().initialArgs()
	if err != nil {
		t.Fatal(err)
	}
	nextArgs.Secrets["NEW"] = "authored.example"
	next, err := prepareInitialState(nextArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.retireJudgeSourcesLocked(t.Context(), next.state); err != nil {
		t.Fatal(err)
	}
	server.publishControlState(next, false)
	server.policyMu.Unlock()
	if result.Target != "authored.example:443" || server.currentEngine().Decide(Request{Host: "authored.example", Port: 443}).Allow {
		t.Fatal("historical public observation changed or granted a target")
	}
}

func TestHoldExportUsesManagementCapacityWhenAllUserSlotsAreFull(t *testing.T) {
	server, _, stream, _, args := exportFixture(t, false)
	for range maxActiveFlows {
		server.flows.sem <- struct{}{}
	}
	defer func() {
		for range maxActiveFlows {
			<-server.flows.sem
		}
	}()
	for n := 1; n < maxActiveFlows; n++ {
		host := "capacity" + strings.Repeat("x", n%30) + "." + strings.Repeat("y", n/30+1) + ".example"
		server.policyMu.Lock()
		_, _, err := server.holds.attach(Request{Host: host, Port: 443}, host, server.now(), time.Minute)
		server.policyMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(server.pendingNetworkSnapshot().Requests) != maxActiveFlows {
		t.Fatal("fixture did not fill actual hold capacity")
	}
	finished := make(chan error, 1)
	go func() {
		finished <- server.writeNetworkHoldResolution(t.Context(), server.ctl, proto.Request{ID: 1, Args: memberJSON(t, args)})
	}()
	response, _ := readExport(t, stream)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !response.OK || len(server.flows.sem) != maxActiveFlows {
		t.Fatal("management export required a 129th user-flow slot")
	}
}

var _ io.Writer = (*exportBlockedConn)(nil)

func TestActualMintPublicationBeforeTheWriterTurnCannotExportTheNewSecret(t *testing.T) {
	initial := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	initial.Initial.Policy = api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}
	server, _, stream, _, _ := exportFixture(t, false, initial)
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	host, err := NormaliseHost(string(value))
	if err != nil {
		t.Fatal(err)
	}
	server.policyMu.Lock()
	ticket, _, err := server.holds.attach(Request{Host: host, Port: 443}, string(value), server.now(), time.Minute)
	server.policyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	args := api.NetworkHoldActionArgs{Version: 1, Session: "export2", Request: ticket.entry.meta.ID, Revision: ticket.entry.meta.Revision, Scope: api.NetworkHoldRepo, Maximum: api.NetworkHoldPublicOnly}
	server.ctlMu.Lock()
	finished := make(chan error, 1)
	go func() {
		finished <- server.writeNetworkHoldResolution(t.Context(), server.ctl, proto.Request{ID: 1, Args: memberJSON(t, args)})
	}()
	server.policyMu.Lock()
	reservation, err := server.reserveResponseSecretLocked(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
	server.policyMu.Unlock()
	if err != nil {
		server.ctlMu.Unlock()
		t.Fatal(err)
	}
	defer reservation.publication.removeWaiter(reservation.waiter)
	server.ctlMu.Unlock()
	response, raw := readExport(t, stream)
	if response.OK || response.Error == nil || response.Error.Code != "network_sensitive_scope" || strings.Contains(string(raw), string(value)) || strings.Contains(string(raw), host) {
		t.Fatal("actual M1 publication leaked its now-sensitive original hold target")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
