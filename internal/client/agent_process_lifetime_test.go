//go:build darwin || linux

package client

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func TestAgentOwnedExitChild(t *testing.T) {
	if os.Getenv("AGENT_OWNED_EXIT_CHILD") != "1" {
		return
	}
	// A real child of this test process exits normally. It cannot contact
	// SSH, read configuration or reach an authentication agent.
}

func TestAgentRawExitRevokesBeforeDeferredDiagnosticsFinish(t *testing.T) {
	agentClientEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	log, err := newAgentNativeLog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentOwnedExitChild$")
	command.Env = append(os.Environ(), "AGENT_OWNED_EXIT_CHILD=1")
	command.WaitDelay = sshWaitDelay
	process, err := CommandProc(command, nil)
	if err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	owner := &sshProc{Proc: process, rawDone: make(chan struct{}), ctx: ctx, logFile: &sshLogFile{agent: log}}
	if err := log.AttachOwner(func(error) { owner.Kill() }); err != nil {
		owner.Kill()
		_ = owner.Wait()
		t.Fatal(err)
	}
	// Hold only the capture's data observer. Lifecycle cancellation and
	// raw exit must not depend on this mutex or the final scan.
	log.mu.Lock()
	release := sync.OnceFunc(log.mu.Unlock)
	defer func() { release(); owner.Kill(); _ = owner.Wait(); _ = log.Close() }()
	waited := make(chan struct{})
	go func() { _ = owner.Wait(); close(waited) }()
	select {
	case <-owner.rawExited():
	case <-time.After(2 * time.Second):
		t.Fatal("the real owned child exit waited for log finalization")
	}
	select {
	case <-waited:
		t.Fatal("the fixture did not retain pending final diagnostics")
	default:
	}
	scope := agentbridge.Scope{Session: "abc234de", Lease: strings.Repeat("1", 32)}
	connection := &Conn{agentOnly: true, proc: owner, done: make(chan struct{}), hostID: strings.Repeat("a", 32), Features: []string{agentbridge.Feature}}
	binding := &peerAgentBinding{wire: []byte("preverified public fixture"), owner: connection, scope: scope, verified: true}
	if binding.matches(connection, scope) {
		t.Fatal("a real exited process retained activation authority during diagnostics")
	}
	ready := make(chan struct{})
	close(ready)
	capture := &agentBindingCollector{owner: owner, ownerCtx: ctx, hostID: connection.HostID(), scope: scope, ready: ready, record: binding.wire}
	if _, err := sealNativeAgentBinding(ctx, capture, connection); err == nil {
		t.Fatal("a real exited process could seal a binding during diagnostics")
	}
	release()
	select {
	case <-waited:
	case <-ctx.Done():
		t.Fatal("owned process/log cleanup did not finish after the fixture released I/O")
	}
}
