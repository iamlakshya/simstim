//go:build linux

package host

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/iamlakshya/simstim/internal/proc"
	"golang.org/x/sys/unix"
)

// This fixture owns a genuine PTY Start, original creation pidfd, Cmd.Wait and
// reader. It cannot construct an active or stopped production session source.
func originalPauseFixtureV2(t *testing.T) *Holder {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	main := prepareHolderMainV1(cmd)
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{cmd: cmd, main: main, ptmx: terminal, waitDone: make(chan struct{}), readDone: make(chan struct{})}
	go func() { _, _ = io.Copy(io.Discard, terminal); close(h.readDone) }()
	go func() { _ = cmd.Wait(); close(h.waitDone) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-h.waitDone
		_ = terminal.Close()
		<-h.readDone
		if main.pidfd >= 0 {
			_ = unix.Close(main.pidfd)
		}
	})
	if main.pidfd < 0 {
		t.Skip("this kernel has no atomic creation pidfd")
	}
	return h
}

func awaitOriginalMainStateV2(t *testing.T, h *Holder, want proc.ChildPidfdStateV2) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		state, err := proc.ObserveChildPidfdV2(h.main.pidfd)
		if err != nil {
			t.Fatal(err)
		}
		if state == want {
			return
		}
		if err := waitMainObservationTurnV2(ctx); err != nil {
			t.Fatalf("original main did not reach native state %v: %v", want, err)
		}
	}
}

func TestOwnedMainPauseRestoresSameActualCreationV2(t *testing.T) {
	h := originalPauseFixtureV2(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pause, err := h.quiesceOwnedMainV2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pause.CheckPaused(ctx); err != nil {
		t.Fatal(err)
	}
	awaitOriginalMainStateV2(t, h, proc.ChildStoppedV2)
	if err := pause.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	awaitOriginalMainStateV2(t, h, proc.ChildContinuedV2)
	if err := h.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("restored original main is not live", err)
	}
	if err := pause.Restore(ctx); err != nil {
		t.Fatal("same restoration was not idempotent", err)
	}
	if h.main.purgePause != nil {
		t.Fatal("restored original main retains a pause obligation")
	}
}

func TestOwnedMainPausePreservesUserStoppedStateV2(t *testing.T) {
	h := originalPauseFixtureV2(t)
	if err := unix.PidfdSendSignal(h.main.pidfd, unix.SIGSTOP, nil, 0); err != nil {
		t.Fatal(err)
	}
	awaitOriginalMainStateV2(t, h, proc.ChildStoppedV2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pause, err := h.quiesceOwnedMainV2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pause.changed || !pause.wasStopped {
		t.Fatal("pre-existing native stop was treated as the purge's change")
	}
	if err := pause.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	awaitOriginalMainStateV2(t, h, proc.ChildStoppedV2)
}

func TestOwnedMainPauseRejectsCancelledPreparationBeforeSignalV2(t *testing.T) {
	h := originalPauseFixtureV2(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pause, err := h.quiesceOwnedMainV2(ctx); pause != nil || err == nil {
		t.Fatal("cancelled preparation acquired an original main pause")
	}
	if h.main.purgePause != nil {
		t.Fatal("cancelled preparation retained a main pause")
	}
	state, err := proc.ObserveChildPidfdV2(h.main.pidfd)
	if err != nil || state == proc.ChildStoppedV2 {
		t.Fatal("cancelled preparation stopped the original main", state, err)
	}
}

func TestOwnedMainPauseCannotRestoreAfterActualJoinV2(t *testing.T) {
	h := originalPauseFixtureV2(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pause, err := h.quiesceOwnedMainV2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.joinOwnedMainV1(); err != nil {
		t.Fatal(err)
	}
	if err := pause.Restore(ctx); err == nil {
		t.Fatal("actual destructive join was relabelled as reversible restoration")
	}
}

func TestOrdinaryActivePreparationCannotOriginateFromMetadataV2(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {id: "abcd2345"}} {
		if source, err := PrepareOrdinaryPurgeForOperationV2(context.Background(), operation); source != nil || err == nil {
			t.Fatal("data-only operation issued active ordinary ownership")
		}
	}
	for _, source := range []*OrdinaryPurgePreparationV2{nil, {}, {quiesced: true}} {
		if err := source.CheckQuiesced(context.Background(), &SessionOperation{id: "abcd2345"}); err == nil {
			t.Fatal("phase metadata supplied actual quiescence")
		}
	}
}
