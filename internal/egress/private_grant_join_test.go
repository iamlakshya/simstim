package egress

import (
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPrivateRevokeJoinsTheActualOwnedFlowBeforeReturning(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"EXPOSED": "privatevalue"}})
	request := Request{Host: "privatevalue.example.test", Port: 443}
	entry, _ := privateQuestion(t, server, request.Host, 443, 1)
	server.policyMu.Lock()
	grant, err := server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldSession, true, "live", request)
	server.policyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	engine := server.currentEngine()
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.DecideWithPermit(request, permit).Allow {
		t.Fatal("fixture did not acquire actual private session rule")
	}
	permit.Commit()
	connection, peer := net.Pipe()
	defer connection.Close()
	defer peer.Close()
	done, read := make(chan struct{}), make(chan []byte, 1)
	flow := server.flows.add(activeFlow{request: request, addr: netip.AddrFrom4([4]byte{203, 0, 113, 9}), permit: permit, cancel: func() { _ = connection.Close() }, done: done})
	go func() {
		defer close(done)
		data, _ := io.ReadAll(peer)
		_ = peer.Close()
		server.flows.remove(flow)
		read <- data
	}()
	server.policyMu.Lock()
	removed, err := server.revokeMemoryGrantLocked(t.Context(), grant.ID)
	server.policyMu.Unlock()
	if err != nil || !removed {
		t.Fatal("actual opaque revocation failed", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("revocation returned before the owned pipe physically ended")
	}
	if data := <-read; len(data) != 0 {
		t.Fatal("revoked owned flow wrote unexpected bytes")
	}
	if server.currentEngine().Decide(request).Allow {
		t.Fatal("revoked private rule retained authority")
	}
}
