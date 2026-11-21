package egress

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func directBudgetFixture(t *testing.T, limit string) (*Server, <-chan net.Conn) {
	t.Helper()
	policy := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "paste.example:8443"})
	policy.Network.UploadBudget = new(limit)
	server := newMemberServer(t, InitArgs{SessionID: "budget234", Agent: "shell", Policy: policy})
	origins := make(chan net.Conn, 4)
	server.dialer.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	}
	server.dialer.DialTCP = func(context.Context, netip.AddrPort) (net.Conn, error) {
		client, origin := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
		origins <- origin
		return client, nil
	}
	return server, origins
}

func directBudgetConnection(t *testing.T, server *Server, ctx context.Context) (net.Conn, *Permit) {
	t.Helper()
	request := Request{Host: "paste.example", Port: 8443, Method: "CONNECT", via: "explicit"}
	permit := server.currentEngine().NewPermit()
	if !server.currentEngine().DecideWithPermit(request, permit).Allow {
		t.Fatal("original normal network grant was not acquired")
	}
	conn, _, _, err := server.dialOrigin(ctx, request.Host, request.Port, permit, &request)
	if err != nil {
		t.Fatal(err)
	}
	permit.Commit()
	return conn, permit
}

func TestUploadBudgetCountsTheActualPartialNativeWritePrefix(t *testing.T) {
	server, origins := directBudgetFixture(t, "16M")
	conn, permit := directBudgetConnection(t, server, t.Context())
	defer conn.Close()
	defer permit.Close()
	origin := <-origins
	read := make(chan error, 1)
	go func() {
		var prefix [17]byte
		_, err := io.ReadFull(origin, prefix[:])
		_ = origin.Close()
		read <- err
	}()
	count, err := conn.Write(make([]byte, 4096))
	if count != 17 || err == nil || <-read != nil {
		t.Fatal("native pipe did not stop after its exact successful prefix")
	}
	server.uploads.mu.Lock()
	row := server.uploads.hosts["paste.example"]
	used, reserved := row.used, row.reserved
	server.uploads.mu.Unlock()
	if used != 17 || reserved != 0 {
		t.Fatal("budget counted an admitted buffer instead of physical successful writes")
	}
}

func TestActualAbandonedBudgetWaitLeavesOnlyOneNextFlowContinuation(t *testing.T) {
	server, origins := directBudgetFixture(t, "0M")
	ctx, cancel := context.WithCancel(t.Context())
	conn, permit := directBudgetConnection(t, server, ctx)
	origin := <-origins
	defer origin.Close()
	written := make(chan error, 1)
	go func() { _, err := conn.Write([]byte{'P'}); written <- err }()
	before := waitUploadHold(t, server)
	cancel()
	if err := <-written; err == nil {
		t.Fatal("abandoned zero-budget write crossed the original native pipe")
	}
	_ = conn.Close()
	permit.Close()
	snapshot := server.pendingNetworkSnapshot()
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].ID != before.ID || snapshot.Requests[0].Waiting != 0 {
		t.Fatal("abandoned physical flow removed or renewed its original question")
	}
	row := snapshot.Requests[0]
	args := api.NetworkHoldActionArgs{Version: 1, Session: "budget234", Request: row.ID, Revision: row.Revision, Scope: api.NetworkHoldOnce, Maximum: api.NetworkHoldPublicOnly}
	if _, err := server.answerNetworkHold(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	next, owner := directBudgetConnection(t, server, t.Context())
	native := <-origins
	received := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := io.ReadFull(native, one[:])
		_ = native.Close()
		received <- err
	}()
	if count, err := next.Write([]byte{'P'}); count != 1 || err != nil || <-received != nil {
		t.Fatal("the original zero-waiter answer did not serve its one next flow", err)
	}
	_ = next.Close()
	owner.Close()
	engine := server.currentEngine()
	fresh := engine.NewPermit()
	defer fresh.Close()
	if engine.evaluate(Request{Host: "paste.example", Port: 8443, uploadBudgetOnly: true}, fresh, evalBudgetReserve).Allow {
		t.Fatal("closing the next flow revived its consumed budget continuation")
	}
}
