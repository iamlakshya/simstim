package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

func init() {
	if os.Getenv(brand.Env("TEST_RAW_AGENT_STDERR")) == "1" {
		_, _ = os.Stderr.Write([]byte("owned stderr fixture\n"))
		os.Exit(0)
	}
}

type heldAgentStderrObserver struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *heldAgentStderrObserver) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}

func TestAgentRawReapPrecedesBlockedDiagnosticWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observer := &heldAgentStderrObserver{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(observer.release) })
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), brand.Env("TEST_RAW_AGENT_STDERR")+"=1")
	cmd.WaitDelay = sshWaitDelay
	process, err := commandAgentProc(cmd, nil, observer)
	if err != nil {
		t.Fatal(err)
	}
	owned := process.(*cmdProc)
	defer process.Kill()
	wait := make(chan error, 1)
	go func() { wait <- process.Wait() }()
	select {
	case <-observer.entered:
	case <-time.After(3 * sshWaitDelay):
		t.Fatal("owned stderr fixture did not reach its diagnostic observer")
	}
	select {
	case <-owned.agentStderr.rawDone:
	case <-time.After(sshWaitDelay):
		t.Fatal("diagnostic writer hid actual process death")
	}
	if exit, ready := owned.observedAgentExit(); !ready || exit != nil {
		t.Fatalf("raw exit: ready=%v err=%v", ready, exit)
	}
	select {
	case err := <-wait:
		if err == nil {
			t.Fatal("unquiesced owned copier was reported complete")
		}
	case <-time.After(3 * sshWaitDelay):
		t.Fatal("owned copier exceeded the original client shutdown budget")
	}
	if owned.agentStderr.failure() == nil {
		t.Fatal("incomplete copier lost its resource witness")
	}
	release.Do(func() { close(observer.release) })
	select {
	case <-owned.agentStderr.done:
	case <-time.After(sshWaitDelay):
		t.Fatal("released owned copier did not unwind")
	}
}

func TestAgentShutdownBudgetDoesNotRestartForLaterPhases(t *testing.T) {
	var budget agentShutdownBudget
	first := time.Now().Add(-3 * sshWaitDelay)
	if got := budget.start(first); !got.Equal(first) {
		t.Fatal("first revocation time changed")
	}
	if got := budget.start(time.Now()); !got.Equal(first) {
		t.Fatal("later retry reset the shutdown budget")
	}
	ctx, stop := budget.context(t.Context(), 2*sshWaitDelay)
	defer stop()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("late diagnostic phase received new grace")
	}
}
