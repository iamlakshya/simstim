package host_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
	"github.com/iamlakshya/simstim/internal/version"
)

type sdkOwnedOutput struct {
	writer *io.PipeWriter
	once   sync.Once
	done   chan struct{}
}

func (o *sdkOwnedOutput) Write(p []byte) (int, error) { return o.writer.Write(p) }
func (o *sdkOwnedOutput) WriteProgress(p []byte, progress func()) (int, error) {
	n, err := o.Write(p)
	if err == nil && n > 0 && progress != nil {
		progress()
	}
	return n, err
}
func (o *sdkOwnedOutput) Cancel() error {
	o.once.Do(func() { _ = o.writer.Close(); close(o.done) })
	return nil
}
func (o *sdkOwnedOutput) Wait() error { <-o.done; return nil }

type sdkBridgeProc struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan struct{}
	err  error
}

func (p *sdkBridgeProc) Stdin() io.WriteCloser { return p.in }
func (p *sdkBridgeProc) Stdout() io.Reader     { return p.out }
func (*sdkBridgeProc) Stderr() string          { return "" }
func (p *sdkBridgeProc) Wait() error           { <-p.done; return p.err }
func (p *sdkBridgeProc) Kill()                 { _ = p.in.Close(); _ = p.out.Close() }

type sdkBridgeTransport struct{ starts atomic.Int32 }

func (t *sdkBridgeTransport) Start(ctx context.Context, _ string) (client.Proc, error) {
	t.starts.Add(1)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &sdkBridgeProc{in: inW, out: outR, done: make(chan struct{})}
	owned := &sdkOwnedOutput{writer: outW, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, p.Kill)
	go func() {
		p.err = host.RunBridgeOwned(inR, owned)
		stop()
		_ = inR.Close()
		_ = owned.Cancel()
		close(p.done)
	}()
	return p, nil
}
func (*sdkBridgeTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("routing fixture must not bootstrap or run a remote command")
}
func (*sdkBridgeTransport) ReachHint() string { return "inspect the routing fixture" }

type sdkBridgeMachine struct{ transport *sdkBridgeTransport }

func (sdkBridgeMachine) Status(context.Context) (string, error) { return client.MachineRunning, nil }
func (sdkBridgeMachine) Start(context.Context) error {
	return errors.New("routing fixture must not wake a host")
}
func (sdkBridgeMachine) Stop(context.Context) error {
	return errors.New("routing fixture must not stop a host")
}
func (sdkBridgeMachine) Destroy(context.Context) error {
	return errors.New("routing fixture must not delete a host")
}
func (m sdkBridgeMachine) Transport(context.Context, bool) (client.Transport, error) {
	return m.transport, nil
}

type sdkBundleSource struct {
	id     string
	closed *atomic.Int32
}

type sdkPeerOwner struct {
	peers   sync.Map
	workers sync.WaitGroup
	closing atomic.Bool
}

func (*sdkBundleSource) PullMax() int64      { return api.PullMaxDefault }
func (*sdkBundleSource) SeedHeads() []string { return nil }
func (s *sdkBundleSource) Capture(_ context.Context, _ pullexport.CaptureArgs, out io.Writer) (pullexport.CaptureResult, error) {
	ref := "refs/heads/" + brand.Name + "/" + s.id + "/topic"
	_, err := io.WriteString(out, "# v2 git bundle\n"+strings.Repeat("b", 40)+" "+ref+"\n\nPACKfixture")
	return pullexport.CaptureResult{DefaultRef: ref, Full: true}, err
}
func (*sdkBundleSource) RecordPulled(context.Context, []string) error { return nil }
func (s *sdkBundleSource) Close() error                               { s.closed.Add(1); return nil }

func TestFiveSequentialLivePrimariesShareOneSDKConnectionAndRetireIdleLines(t *testing.T) {
	fixture := hosttest.New(t)
	transport := &sdkBridgeTransport{}
	client.RegisterProvider("pull-sdk-fixture", func(client.Host) (client.Lifecycle, error) { return sdkBridgeMachine{transport}, nil })
	ch := client.Host{Name: "pull-fixture", Machine: &client.Machine{Provider: "pull-sdk-fixture", ID: "owned"}}
	var active, maximum, closed atomic.Int32
	peers := &sdkPeerOwner{}
	var listeners []net.Listener
	var ids []string
	for _, letter := range []string{"a", "b", "c", "d", "e", "f"} {
		id := strings.Repeat(letter, 8)
		ids = append(ids, id)
		m := &host.Manifest{ID: id, Name: "pull-fixture-" + letter, Cwd: "/work/repo", Wall: &host.WallRecord{Driver: "process"}}
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(paths.Socket(id)), 0700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", paths.Socket(id))
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		peers.workers.Add(1)
		go func() { defer peers.workers.Done(); sdkPullSupervisor(t, listener, &active, &maximum, &closed, peers) }()
	}
	// No fixture row may reach hosttest's real sandbox cleanup.
	t.Cleanup(func() {
		peers.closing.Store(true)
		for _, listener := range listeners {
			_ = listener.Close()
		}
		peers.peers.Range(func(key, value any) bool { _ = key.(net.Conn).Close(); return true })
		peers.workers.Wait()
		for _, id := range ids {
			_ = os.RemoveAll(paths.SessionDir(id))
		}
	})
	ctx := client.WithoutWake(client.WithoutInstall(t.Context()))
	connection, err := client.Dial(ctx, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for index, id := range ids {
		token := strings.Repeat(string(rune('a'+index)), 26)
		var descriptor api.PullExport
		if err = connection.Call(ctx, api.OpPullBegin, api.PullBeginArgs{ID: id, Token: token}, &descriptor); err != nil {
			t.Fatal("sequential primary was rejected", index, err)
		}
		var chunk api.PullChunk
		if err = connection.Call(ctx, api.OpPullRead, api.PullReadArgs{ID: id, Token: token}, &chunk); err != nil || !chunk.EOF {
			t.Fatal(chunk, err)
		}
		var ack api.PulledResult
		if err = connection.Call(ctx, api.OpPulled, api.PulledArgs{ID: id, Token: token, Commits: []string{strings.Repeat("b", 40)}}, &ack); err != nil {
			t.Fatal(err)
		}
		// Peer EOF processing can trail the local owned reader join. Wait for
		// that causal cleanup, rather than treating scheduler lag as a leak.
		until := time.Now().Add(time.Second)
		for active.Load() > api.PullExportsMax && time.Now().Before(until) {
			time.Sleep(time.Millisecond)
		}
		if active.Load() > api.PullExportsMax {
			t.Fatal("completed idle peers accumulated", active.Load())
		}
	}
	if transport.starts.Load() != 1 || closed.Load() != int32(len(ids)) {
		t.Fatal("a lifetime quota/reconnect or retained line leak replaced idle cleanup", transport.starts.Load(), closed.Load(), maximum.Load())
	}
	_ = fixture // owns private HOME/XDG and outside-peer setup
}

// This is a normal RPC fixture, not a namespace or backend proof. It delegates
// domain handling to the SAME PullRPC and carries no private framing/parser.
func sdkPullSupervisor(t *testing.T, listener net.Listener, active, maximum, closed *atomic.Int32, peers *sdkPeerOwner) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		if peers.closing.Load() {
			_ = conn.Close()
			return
		}
		peers.peers.Store(conn, true)
		if peers.closing.Load() {
			_ = conn.Close()
		}
		peers.workers.Add(1)
		go func() {
			defer peers.workers.Done()
			defer peers.peers.Delete(conn)
			defer conn.Close()
			reader, writer := bufio.NewReader(conn), proto.NewWriter(conn)
			frame, err := proto.Read(reader)
			if err != nil || frame.Type != proto.THello {
				return
			} // alive probe has no hello
			count := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); count > old && !maximum.CompareAndSwap(old, count); old = maximum.Load() {
			}
			if err = writer.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor", Features: []string{api.FeaturePullBundle, api.FeaturePullOutputProgress}}); err != nil {
				return
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rpc, err := host.NewPullRPC(ctx, func(_ context.Context, original, primary string) (pullexport.Source, error) {
				if original != primary {
					return nil, fmt.Errorf("fixture primary changed")
				}
				return &sdkBundleSource{id: primary, closed: closed}, nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			defer rpc.Close()
			for {
				frame, err = proto.Read(reader)
				if err != nil {
					return
				}
				if frame.Type == proto.TEvent {
					if progress, err := api.DecodePullOutputProgress(frame.Payload); err == nil {
						rpc.Touch(progress)
					}
					continue
				}
				if frame.Type != proto.TRequest {
					return
				}
				var request proto.Request
				if json.Unmarshal(frame.Payload, &request) != nil {
					return
				}
				run, err := rpc.Prepare(request)
				if err != nil {
					_ = writer.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, Error: &proto.Error{Code: "fixture", Message: err.Error()}})
					continue
				}
				go func(request proto.Request) {
					value, err := run(rpc.Context())
					response := proto.Response{ID: request.ID, OK: err == nil}
					if err != nil {
						response.Error = &proto.Error{Code: "fixture", Message: err.Error()}
					} else {
						response.Result, _ = json.Marshal(value)
					}
					_ = writer.WriteJSON(proto.TResponse, response)
				}(request)
			}
		}()
	}
}
