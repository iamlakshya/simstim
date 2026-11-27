//go:build darwin || linux

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

type agentMemoryPeer struct {
	conn     net.Conn
	enc      *json.Encoder
	events   chan agentMemoryEvent
	err      chan error
	done     chan struct{}
	pending  []agentMemoryEvent
	terminal error
}

func agentMemoryListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func agentMemoryAccept(ctx context.Context, l *net.UnixListener) (*agentMemoryPeer, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = l.SetDeadline(deadline)
	}
	c, err := l.AcceptUnix()
	if err != nil {
		return nil, err
	}
	p := &agentMemoryPeer{conn: c, enc: json.NewEncoder(c), events: make(chan agentMemoryEvent, 32), err: make(chan error, 1), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		d := json.NewDecoder(bufio.NewReaderSize(c, 4096))
		for {
			var e agentMemoryEvent
			if err := d.Decode(&e); err != nil {
				p.err <- err
				return
			}
			p.events <- e
		}
	}()
	return p, nil
}

func (p *agentMemoryPeer) command(c agentMemoryCommand) error { return p.enc.Encode(c) }
func (p *agentMemoryPeer) wait(ctx context.Context, event, phase string) (agentMemoryEvent, error) {
	for i, e := range p.pending {
		if e.Event == event && (phase == "" || phase == e.Phase) {
			p.pending = append(p.pending[:i], p.pending[i+1:]...)
			return e, nil
		}
	}
	if p.terminal != nil {
		return agentMemoryEvent{}, p.terminal
	}
	for {
		select {
		case <-ctx.Done():
			return agentMemoryEvent{}, ctx.Err()
		case err := <-p.err:
			p.terminal = err
			// The decoder can report EOF immediately after its final event.
			// Preserve that event instead of selecting the EOF at random.
			for {
				select {
				case e := <-p.events:
					if e.Event == event && (phase == "" || phase == e.Phase) {
						return e, nil
					}
					p.pending = append(p.pending, e)
				default:
					return agentMemoryEvent{}, err
				}
			}
		case e := <-p.events:
			if e.Event == event && (phase == "" || phase == e.Phase) {
				return e, nil
			}
			p.pending = append(p.pending, e)
		}
	}
}

// TestAgentModePerProcessHeapBudget keeps one measured client process and
// one never-reset baseline across three full valid activation cycles.
func TestAgentModePerProcessHeapBudget(t *testing.T) {
	options := agentbridge.Options()
	if agentFWBufferSize != 32<<10 || agentFWMaxBody != 256<<10 || agentNativeLogBuffer != 32<<10 || agentNativeLogLine != 64<<10 || agentNativeLogTotal != 1<<20 || agentNativeLogDisplayLimit != 8<<10 || agentbridge.MaxIdentityHello != 16<<10 || agentbridge.MaxStreams != 16 || options.Window != 64<<10 || options.MaxGuestStreams != 16 || options.MaxSupervisorStreams != 1 {
		t.Fatal("an agent-mode fixed resource bound changed")
	}
	for _, shape := range []string{"flat-padding", "dense-short-names", "rsa-certificate", "rsa-ca-maximum", "hostile-principals", "nested-ca-bomb"} {
		t.Run(shape, func(t *testing.T) { agentMemoryBudgetCase(t, shape) })
	}
}

func agentMemoryBudgetCase(t *testing.T, shape string) {
	t.Helper()
	cfg, _, env := agentMemoryFixture(t, shape)
	clientListener := agentMemoryListener(t, cfg.ClientControl)
	hostListener := agentMemoryListener(t, cfg.HostControl)
	nativeListener := agentMemoryListener(t, cfg.NativeControl)
	// Scope expanded from one90s cycle to three90s cycles. The total proof
	// guard is270s. Production and per-operation deadlines are unchanged.
	ctx, cancel := context.WithTimeout(t.Context(), 270*time.Second)
	defer cancel()
	stderr, err := os.Create(filepath.Join(cfg.Root, "child-stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, cfg.Binary)
	cmd.Env = env
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	run := &agentMemoryRun{t: t, ctx: ctx, cfg: cfg, hostListener: hostListener, nativeListener: nativeListener}
	waited := false
	defer func() {
		for _, p := range run.peers {
			_ = p.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = p.command(agentMemoryCommand{Command: "stop"})
		}
		for _, p := range run.peers {
			_ = p.conn.Close()
			<-p.done
		}
		if !waited {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				cancel()
				<-done
				t.Error("memory helper needed owned-process cancellation fallback")
			}
		}
		cancel()
		if t.Failed() {
			if raw, err := os.ReadFile(filepath.Join(cfg.Root, "child-stderr")); err == nil {
				t.Logf("owned child stderr: %s", raw)
			}
		}
	}()
	beginCtx, beginCancel := context.WithTimeout(ctx, 90*time.Second)
	run.client = run.accept(beginCtx, clientListener, "client")
	run.command(run.client, agentMemoryCommand{Command: "begin"})
	run.baseline = run.wait(beginCtx, run.client, "baseline", "")
	beginCancel()
	if run.baseline.PID == os.Getpid() {
		t.Fatal("client heap was measured in the parent")
	}
	completed := 0
	for cycle := range 3 {
		cycleCtx, stop := context.WithTimeout(ctx, 90*time.Second)
		valid := run.cycle(cycleCtx, cycle)
		stop()
		if valid {
			completed++
		}
	}
	if !cfg.RequireRefusal && !cfg.AllowRefusal && completed != 3 {
		t.Fatalf("completed%d valid16-stream cycles, want3", completed)
	}
	run.command(run.client, agentMemoryCommand{Command: "stop"})
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(fmt.Errorf("measured client process: %w", err))
	}
	t.Logf("completed cycles=%d native-grammar=%v; native shim is resource-only", completed, cfg.NativeGrammar)
}
