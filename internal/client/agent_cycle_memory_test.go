//go:build darwin || linux

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

type agentMemoryRun struct {
	t                            *testing.T
	ctx                          context.Context
	cfg                          agentMemoryConfig
	client                       *agentMemoryPeer
	hostListener, nativeListener *net.UnixListener
	peers                        []*agentMemoryPeer
	baseline                     agentMemoryEvent
	lastProjection, lastSDK      int64
	lastRejectedCapture          int64
}

func (r *agentMemoryRun) accept(ctx context.Context, l *net.UnixListener, role string) *agentMemoryPeer {
	r.t.Helper()
	p, err := agentMemoryAccept(ctx, l)
	if err != nil {
		r.t.Fatalf("accept%s: %v", role, err)
	}
	r.peers = append(r.peers, p)
	e := r.wait(ctx, p, "hello", "")
	if e.Role != role {
		r.t.Fatalf("wrong role %+v", e)
	}
	return p
}
func (r *agentMemoryRun) command(p *agentMemoryPeer, c agentMemoryCommand) {
	r.t.Helper()
	if err := p.command(c); err != nil {
		r.t.Fatal(err)
	}
}
func (r *agentMemoryRun) wait(ctx context.Context, p *agentMemoryPeer, event, phase string) agentMemoryEvent {
	r.t.Helper()
	e, err := p.wait(ctx, event, phase)
	if err != nil {
		r.t.Fatalf("wait%s/%s: %v", event, phase, err)
	}
	if e.Error != "" {
		r.t.Fatalf("%s %s: %s", e.Role, event, e.Error)
	}
	return e
}
func (r *agentMemoryRun) sample(ctx context.Context, p *agentMemoryPeer, phase string) agentMemoryEvent {
	r.t.Helper()
	r.command(p, agentMemoryCommand{Command: "sample", Phase: phase})
	e, err := p.wait(ctx, "sample", phase)
	if err != nil {
		r.t.Fatal(err)
	}
	r.observe(e)
	return e
}
func (r *agentMemoryRun) observe(e agentMemoryEvent) {
	r.t.Helper()
	raw, _ := json.Marshal(e)
	r.t.Logf("actual per-process observation: %s", raw)
	if e.Error != "" {
		r.t.Error(e.Error)
	}
	if e.Role == "client" && e.Event == "sample" && (e.PID != r.baseline.PID || e.Baseline != r.baseline.Baseline) {
		r.t.Fatal("client process or original baseline changed")
	}
}

func (r *agentMemoryRun) cycle(ctx context.Context, cycle int) bool {
	r.t.Helper()
	phase := fmt.Sprintf("cycle-%d", cycle)
	source := newAgentMemorySource(r.t, r.cfg)
	defer source.Close()
	r.command(r.client, agentMemoryCommand{Command: "start", Cycle: cycle})
	native := r.accept(ctx, r.nativeListener, "native")
	r.command(native, agentMemoryCommand{Command: "begin"})
	remote := r.accept(ctx, r.hostListener, "host")
	r.command(remote, agentMemoryCommand{Command: "begin"})
	hostBase := r.wait(ctx, remote, "baseline", "")
	if hostBase.PID == r.baseline.PID || hostBase.PID == os.Getpid() {
		r.t.Fatal("host is not an independent measured process")
	}
	prepared, err := r.client.wait(ctx, "prepared", phase)
	if err != nil {
		r.t.Fatal(err)
	}
	r.sample(ctx, r.client, phase+"/complete-maximum-native-startup")
	r.sample(ctx, remote, phase+"/complete-host-startup")
	if prepared.Error != "" {
		if !(r.cfg.RequireRefusal || r.cfg.AllowRefusal) || !prepared.Refused {
			r.t.Fatal(prepared.Error)
		}
		if r.cfg.RequireRefusal {
			r.command(r.client, agentMemoryCommand{Command: "reject-profile", Phase: phase})
			e, err := r.client.wait(ctx, "profile", phase)
			if err != nil {
				r.t.Fatal(err)
			}
			r.observe(e)
			if e.Profile.CollectorAlloc <= r.lastRejectedCapture {
				r.t.Fatal("a repeated refusal did not capture a fresh maximum record")
			}
			r.lastRejectedCapture = e.Profile.CollectorAlloc
			source.mu.Lock()
			bound := source.boundCount
			source.mu.Unlock()
			if bound != 0 {
				r.t.Fatal("a refused binding opened a source operation")
			}
		}
		r.t.Logf("hostile input typed-refused before publication: %s", prepared.Error)
		r.closeCycle(ctx, remote, phase)
		return false
	}
	if r.cfg.RequireRefusal {
		r.t.Fatal("malformed nested CA reached a prepared capability")
	}
	r.command(r.client, agentMemoryCommand{Command: "activate", Phase: phase})
	r.wait(ctx, r.client, "active", phase)
	drivers := make([]*net.UnixConn, 0, 16)
	var driverWorkers sync.WaitGroup
	defer func() {
		for _, c := range drivers {
			_ = c.Close()
		}
		driverWorkers.Wait()
	}()
	for range 16 {
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: r.cfg.AgentPath, Net: "unix"})
		if err != nil {
			r.t.Fatal(err)
		}
		if err := c.SetWriteBuffer(4096); err != nil {
			r.t.Fatal(err)
		}
		if err := c.SetReadBuffer(4096); err != nil {
			r.t.Fatal(err)
		}
		drivers = append(drivers, c)
	}
	pending := map[string]int{}
	if err := agentMemoryWaitSource(ctx, source, "bound", 16, pending); err != nil {
		r.t.Fatal(err)
	}
	gate := func(direction, state string) {
		r.command(native, agentMemoryCommand{Command: "gate", Gate: direction, Phase: state})
		r.wait(ctx, native, "gate", direction+":"+state)
	}
	settle := func() {
		select {
		case <-ctx.Done():
			r.t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	gate("h2c", "closed")
	for _, c := range drivers {
		driverWorkers.Go(func() {
			var buffer [32 << 10]byte
			for range 3 {
				if err := agentMemoryPacket(c, agentFWSign, buffer[:]); err != nil {
					return
				}
			}
		})
	}
	settle()
	r.sample(ctx, remote, phase+"/sixteen-blocked-request-windows")
	gate("h2c", "open")
	r.wait(ctx, native, "ingress", "")
	r.sample(ctx, r.client, phase+"/sixteen-input-windows-before-source-ack")
	close(source.ack)
	if err := agentMemoryWaitSource(ctx, source, "sign1", 16, pending); err != nil {
		r.t.Fatal(err)
	}
	gate("c2h", "closed")
	close(source.reply)
	if err := agentMemoryWaitSource(ctx, source, "reply-start1", 16, pending); err != nil {
		r.t.Fatal(err)
	}
	settle()
	r.sample(ctx, r.client, phase+"/sixteen-blocked-response-windows")
	fresh := r.profile(ctx, r.client, phase)
	if fresh.Profile.ProjectionAllocBytes <= r.lastProjection || fresh.Profile.SDKCertificateAllocBytes <= r.lastSDK {
		r.t.Fatal("a fresh capture did not exercise the projected SDK certificate path")
	}
	gate("c2h", "open")
	settle()
	r.sample(ctx, remote, phase+"/sixteen-bidirectional-relay-windows")
	// Both wire gates are open. Agent and relay buffers remain resident.
	// Actual policy validation and every heartbeat complete before sampling.
	r.command(r.client, agentMemoryCommand{Command: "policy-checks", Phase: phase + "/maximum-policy-thirty-two-checks"})
	e, err := r.client.wait(ctx, "sample", phase+"/maximum-policy-thirty-two-checks")
	if err != nil {
		r.t.Fatal(err)
	}
	r.observe(e)
	if e.PolicyChecks != 32 || e.Pings != 32 {
		r.t.Fatalf("actual checks=%d,pings=%d, want32each", e.PolicyChecks, e.Pings)
	}
	last := r.profile(ctx, r.client, phase)
	r.lastProjection, r.lastSDK = last.Profile.ProjectionAllocBytes, last.Profile.SDKCertificateAllocBytes
	r.profile(ctx, remote, phase)
	r.closeCycle(ctx, remote, phase)
	return true
}

func (r *agentMemoryRun) profile(ctx context.Context, p *agentMemoryPeer, phase string) agentMemoryEvent {
	r.t.Helper()
	r.command(p, agentMemoryCommand{Command: "profile", Phase: phase})
	e, err := p.wait(ctx, "profile", phase)
	if err != nil {
		r.t.Fatal(err)
	}
	r.observe(e)
	return e
}
func (r *agentMemoryRun) closeCycle(ctx context.Context, remote *agentMemoryPeer, phase string) {
	r.t.Helper()
	r.command(remote, agentMemoryCommand{Command: "stop"})
	e, err := remote.wait(ctx, "sample", "shutdown")
	if err != nil {
		r.t.Fatal(err)
	}
	r.observe(e)
	r.command(r.client, agentMemoryCommand{Command: "close", Phase: phase + "/closed-native-classification"})
	e, err = r.client.wait(ctx, "sample", phase+"/closed-native-classification")
	if err != nil {
		r.t.Fatal(err)
	}
	r.observe(e)
}
