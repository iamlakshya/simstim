package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func boundRequestScope(t *testing.T, server *Server, names []string) *originRequest {
	t.Helper()
	conn, peer := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	scope := &originRequest{s: server, request: Request{Host: "example.com", Port: 443, Method: "GET", Path: "/"}, permit: server.currentEngine().NewPermit(), ctx: ctx, cancel: cancel, conn: conn, http2: true}
	if err := scope.bindResponseWriter(&boundDeadlineWriter{}); err != nil {
		t.Fatal(err)
	}
	if !scope.track() {
		t.Fatal("request fixture did not register physical ownership")
	}
	if names != nil {
		server.policyMu.Lock()
		err := scope.tagSecretNamesLocked(names)
		server.policyMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { scope.close(); _ = peer.Close() })
	return scope
}

func TestBoundArmWaitsForPhysicalRequestFinaliserAndKeepsUnrelatedStreams(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000", "OTHER": "WXYZ1111"}))
	changed, sibling := boundRequestScope(t, server, []string{"KEY"}), boundRequestScope(t, server, []string{"OTHER"})
	finish := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(finish) }) })
	go func() { <-changed.ctx.Done(); <-finish; changed.close() }()
	before := server.currentMembers()
	done := make(chan error, 1)
	go func() {
		_, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "EFGH2222"}}))
		done <- err
	}()
	select {
	case <-changed.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("rotation did not cancel the exact request owner")
	}
	select {
	case <-done:
		t.Fatal("rotation treated cancel as completed physical finalisation")
	default:
	}
	if before != server.currentMembers() || sibling.ctx.Err() != nil {
		t.Fatal("rotation published too early or closed an unrelated HTTP/2 stream")
	}
	release.Do(func() { close(finish) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("physical completion did not release the secret transaction")
	}
	if before == server.currentMembers() || sibling.ctx.Err() != nil {
		t.Fatal("completed rotation failed to publish or disturbed its sibling")
	}
}

func TestBoundRotationCancelsQueuedMintCallbackBeforeJoiningItsRequest(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	request := boundRequestScope(t, server, nil)
	next, err := cloneMemberState(server.currentMembers())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := applyBoundUpdates(t.Context(), next, map[string]api.Secret{"KEY": "EFGH2222"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareBoundRegistry(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	prepared.secretOnly = true
	server.policyMu.Lock()
	defer server.policyMu.Unlock()
	callback := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
		_, err := server.registerResponseSecret(request.ctx, "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
		callback <- err
	}()
	<-started
	select {
	case <-callback:
		t.Fatal("response callback crossed current mutation ownership")
	case <-time.After(10 * time.Millisecond):
	}
	ownerDone := make(chan struct{})
	go func() { <-request.ctx.Done(); <-callback; request.close(); close(ownerDone) }()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := server.closeSecretResources(ctx, changed, []string{"example.com:443"}, nil); err != nil {
		t.Fatal("queued mint prevented the actual request from joining")
	}
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("resource join returned before the callback and physical finaliser")
	}
	server.publishControlState(prepared, false)
	if len(server.currentMembers().secrets["KEY"].minted) != 0 {
		t.Fatal("cancelled old response published a candidate during rotation")
	}
}

func TestNewBoundDestinationClosesAndJoinsItsActualOpaqueCarrier(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	conn, guest := net.Pipe()
	upstream, origin := net.Pipe()
	t.Cleanup(func() { _ = guest.Close(); _ = origin.Close() })
	hdr := StreamHeader{Via: "transparent", Host: "fresh.example.com", Port: 443}
	ctx, finish, err := server.beginSecretCarrier(t.Context(), conn, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if inspect, err := server.secretCarrierInspection(ctx, hdr); err != nil || inspect {
		t.Fatal("unbound carrier fixture unexpectedly required inspection")
	}
	permit := server.currentEngine().NewPermit()
	id, _ := server.registerSecretFlow(ctx, conn, upstream, Request{Host: hdr.Host, Port: hdr.Port, Method: "TCP"}, netip.Addr{}, permit)
	if id == 0 {
		t.Fatal("opaque resource lost its exact carrier association")
	}
	defer server.flows.remove(id)
	intent := memberIntent(initial.Generation, map[string]api.Secret{"FRESH": "WXYZ1111"}, "")
	intent.Binds["FRESH"] = api.SecretBind{Hosts: []string{hdr.Host}, Header: "Authorization", Format: "Bearer {value}"}
	before := server.currentMembers()
	done := make(chan error, 1)
	go func() { _, err := server.addBoundControlMember(memberJSON(t, intent)); done <- err }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("new binding did not close its old opaque carrier")
	}
	if before != server.currentMembers() {
		t.Fatal("new binding published before outer carrier finalisation")
	}
	// The caller runs IO/audit/permit finalisers before its deferred finish.
	server.flows.remove(id)
	permit.Close()
	finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("real carrier completion did not release binding publication")
	}
}

func TestMissingOpaqueOwnershipIsKnownNegativeBeforeAnyDisruption(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	conn, guest := net.Pipe()
	upstream, origin := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = guest.Close(); _ = upstream.Close(); _ = origin.Close() })
	permit := server.currentEngine().NewPermit()
	id, _ := server.registerFlow(conn, upstream, Request{Host: "fresh.example.com", Port: 443, Method: "TCP"}, netip.Addr{}, permit)
	defer server.flows.remove(id)
	err := server.closeSecretResources(t.Context(), nil, nil, []string{"fresh.example.com:443"})
	requireBoundError(t, err, "secret_lifetime")
	write := make(chan error, 1)
	go func() { _, err := io.WriteString(conn, "alive"); write <- err }()
	_ = guest.SetReadDeadline(time.Now().Add(time.Second))
	data := make([]byte, 5)
	if _, err := io.ReadFull(guest, data); err != nil || string(data) != "alive" {
		t.Fatal("known-negative ownership validation disturbed the old carrier")
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
}

func TestPostAbortJoinUncertaintyClosesOnlyOwnedEgressAndKeepsOldRegistry(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	request := boundRequestScope(t, server, []string{"KEY"})
	before := server.currentMembers()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	err := server.closeSecretResources(ctx, []string{"KEY"}, nil, nil)
	requireBoundError(t, err, "secret_lifetime_uncertain")
	if request.ctx.Err() == nil || before != server.currentMembers() {
		t.Fatal("uncertain cleanup claimed untouched resources or published a new registry")
	}
	select {
	case <-server.sess.Done():
	default:
		t.Fatal("uncertain physical completion retained the owned network channel")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("join uncertainty did not reach its controlled cancellation boundary")
	}
}
