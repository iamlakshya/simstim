package supervisor

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPortSocketIsPrivateAndRevocationClosesExistingFlow(t *testing.T) {
	testFolders(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	local, remote := net.Pipe()
	session, guest := link.New(local, link.Supervisor, link.Options{}), link.New(remote, link.Guest, link.Options{})
	defer session.Close()
	defer guest.Close()
	policy := api.Policy{Order: []api.LayerRef{{Kind: api.LayerSession}}, Rules: []api.Rule{{Door: api.DoorPort, Layer: api.LayerSession, Target: "3000", Allow: true}}}
	s := &Supervisor{id: "port2345", lifetime: ctx, link: session, m: &host.Manifest{Policy: &policy}, rec: record{phase: host.LifeRunning}}
	defer s.closePortListeners(true)
	if err := s.reconcilePorts(); err != nil {
		t.Fatal(err)
	}
	name := paths.PortSocket(s.id, 3000)
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("private Unix socket = %v, %v", info, err)
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", name)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	stream, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var header proto.PortHeaderV1
	if stream.Kind() != link.KindPort || stream.Header(&header) != nil || header.Version != 1 || header.Port != 3000 {
		t.Fatal("forward selected another guest destination")
	}
	payload := []byte{0, '\x1b', '[', '3', '1', 'm', '\n', 255}
	written := make(chan error, 1)
	go func() { _, err := connection.Write(payload); written <- err }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	for index := range got {
		if got[index] != payload[index] {
			t.Fatalf("port byte %d changed", index)
		}
	}
	s.mu.Lock()
	s.m.Policy.Rules[0].Allow = false
	s.mu.Unlock()
	if err := s.reconcilePorts(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatalf("revoked port socket remains: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := connection.Read(got[:1]); err == nil {
		t.Fatal("revoked connection remained readable")
	}
}

func TestForwardViewContainsOnlyActualLiveOwners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{portListeners: map[int]*portForward{3000: {ctx: ctx}}, reverseForwards: map[string]*reverseForward{"pending": {ctx: ctx, args: proto.ReversePortV1{PortHeaderV1: proto.PortHeaderV1{Version: 1, Port: 5432}}}}}
	view := s.portForwardView()
	if view.Version != 1 || len(view.Forwards) != 1 || view.Forwards[0].Port != 3000 || view.Forwards[0].Reverse {
		t.Fatalf("pending reverse became a row fact: %+v", view)
	}
	cancel()
	if got := s.portForwardView(); len(got.Forwards) != 0 {
		t.Fatalf("revoked owner remained a row fact: %+v", got)
	}
}
