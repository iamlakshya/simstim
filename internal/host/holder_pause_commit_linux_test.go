//go:build linux

package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestActualPausedMainCannotWriteFromHUPAfterCommitV2(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late-write")
	command := exec.Command("/bin/sh", "-c", `trap 'printf late > "$1"; exit 0' HUP; printf 'ready\n'; while :; do sleep 1; done`, "original-main", marker)
	main := prepareHolderMainV1(command)
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{cmd: command, main: main, ptmx: terminal, waitDone: make(chan struct{}), readDone: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(h.readDone)
		reader := bufio.NewReader(terminal)
		_, err := reader.ReadString('\n')
		ready <- err
		_, _ = io.Copy(io.Discard, reader)
	}()
	go func() { _ = command.Wait(); close(h.waitDone) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-h.waitDone
		_ = terminal.Close()
		<-h.readDone
		if main.pidfd >= 0 {
			_ = unix.Close(main.pidfd)
		}
	})
	if main.pidfd < 0 {
		t.Skip("this kernel has no atomic original creation pidfd")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	pause, err := h.quiesceOwnedMainV2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pause.CheckPaused(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.joinOwnedMainV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original main ran a HUP write after its held-pause commit", err)
	}
	status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("actual paused creation did not join through the destructive commit", command.ProcessState)
	}
	if err := pause.Restore(ctx); err == nil {
		t.Fatal("destructively committed pause claimed restoration")
	}
}
