package host

import (
	"bufio"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
	"github.com/iamlakshya/simstim/internal/version"
)

type startupReadBoundary struct {
	net.Conn
	reads   atomic.Int32
	blocked chan struct{}
}

func (c *startupReadBoundary) Read(p []byte) (int, error) {
	// The client writes the whole hello as one frame. The second read is
	// therefore the pending handler waiting for its first post-hello frame.
	if c.reads.Add(1) == 2 {
		close(c.blocked)
	}
	return c.Conn.Read(p)
}

func TestPendingControlDispatchesFrameReadAcrossActivation(t *testing.T) {
	for _, hook := range []bool{false, true} {
		t.Run(map[bool]string{false: "attach", true: "hook"}[hook], func(t *testing.T) {
			m := startupTestManifest(t)
			p := newHolderStartup(m.ID)
			server, client := net.Pipe()
			boundary := &startupReadBoundary{Conn: server, blocked: make(chan struct{})}
			p.connections[boundary] = struct{}{}
			p.serves.Add(1)
			worker := ownedStartupWorker(t, func() struct{} { p.serve(boundary, hook); return struct{}{} }, func() {
				_ = client.Close()
				_ = server.Close()
				p.cancel()
			})
			t.Cleanup(func() { _ = client.Close(); _ = server.Close(); p.cancel(); worker.wait(t) })
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			writer, reader := proto.NewWriter(client), bufio.NewReader(client)
			role := "bridge"
			if hook {
				role = roleHook
			}
			if err := writer.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: role}); err != nil {
				t.Fatal(err)
			}
			if f, err := proto.Read(reader); err != nil || f.Type != proto.THello {
				t.Fatalf("pending hello = %v, %v", f.Type, err)
			}
			awaitStartupTest(t, boundary.blocked)
			h := &Holder{id: m.ID, m: m, log: p.peer.log, ring: ring.New(1024), clients: map[*hclient]struct{}{}, exited: true}
			p.activate(h)
			if hook {
				args, err := json.Marshal(HookEvent{Event: "fixture-no-op"})
				if err != nil {
					t.Fatal(err)
				}
				if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "hook", Args: args}); err != nil {
					t.Fatal(err)
				}
				f, err := proto.Read(reader)
				var response proto.Response
				if err != nil || f.Type != proto.TResponse || json.Unmarshal(f.Payload, &response) != nil || !response.OK {
					t.Fatalf("crossing hook frame was dropped or refused: %v, %s, %v", f.Type, f.Payload, err)
				}
			} else {
				if err := writer.WriteJSON(proto.TAttach, proto.Attach{LastOffset: -1}); err != nil {
					t.Fatal(err)
				}
				if f, err := proto.Read(reader); err != nil || f.Type != proto.TAttached {
					t.Fatalf("crossing attach frame was dropped or refused: %v, %v", f.Type, err)
				}
				if f, err := proto.Read(reader); err != nil || f.Type != proto.TEnded {
					t.Fatalf("crossing attach did not complete exactly once: %v, %v", f.Type, err)
				}
			}
			// A duplicate dispatch would leave another first-frame response
			// ahead of this request, so its ID must be exactly two.
			op := "sockets"
			if hook {
				op = "hook"
			}
			if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: op, Args: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			frame, err := proto.Read(reader)
			var response proto.Response
			if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || !response.OK || response.ID != 2 {
				t.Fatalf("crossing frame was duplicated: %v, %s, %v", frame.Type, frame.Payload, err)
			}
			_ = client.Close()
			worker.wait(t)
		})
	}
}
