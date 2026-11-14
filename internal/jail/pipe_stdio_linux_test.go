package jail

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInternalPipesGiveTheChildBlockingDescriptors(t *testing.T) {
	for n, name := range streams {
		t.Run(name, func(t *testing.T) {
			c := lifecycleCmd(t)
			caller, err := c.pipe(n)
			if err != nil {
				t.Fatal(err)
			}
			defer c.closeEnds(true)
			callerFlags := descriptorFlags(t, caller, unix.F_GETFL)
			if callerFlags&unix.O_NONBLOCK == 0 || descriptorFlags(t, c.pipes[n], unix.F_GETFL)&unix.O_NONBLOCK == 0 {
				t.Fatal("the Go pipe fixture must initially be nonblocking at both endpoints")
			}
			_, release, err := c.stdio()
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			child := [3]any{c.cmd.Stdin, c.cmd.Stdout, c.cmd.Stderr}[n].(*os.File)
			_ = child.Fd() // exercise the exec package's descriptor extraction
			if flags := descriptorFlags(t, child, unix.F_GETFL); flags&unix.O_NONBLOCK != 0 {
				t.Errorf("the child's %s remains nonblocking: %#x", name, flags)
			}
			if flags := descriptorFlags(t, caller, unix.F_GETFL); flags != callerFlags {
				t.Errorf("preparing child %s changed the caller endpoint from %#x to %#x", name, callerFlags, flags)
			}
		})
	}
}

func TestExplicitCallerPipeFlagsStayUnchanged(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	readFlags, writeFlags := descriptorFlags(t, read, unix.F_GETFL), descriptorFlags(t, write, unix.F_GETFL)
	c := lifecycleCmd(t)
	c.Stdin, c.Stdout, c.Stderr = read, write, write
	c.files = []*os.File{read, write}
	files, release, err := c.stdio()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, file := range append(files, c.cmd.Stdin.(*os.File), c.cmd.Stdout.(*os.File), c.cmd.Stderr.(*os.File)) {
		_ = file.Fd()
	}
	if flags := descriptorFlags(t, read, unix.F_GETFL); flags != readFlags {
		t.Errorf("the caller-provided read endpoint changed from %#x to %#x", readFlags, flags)
	}
	if flags := descriptorFlags(t, write, unix.F_GETFL); flags != writeFlags {
		t.Errorf("the caller-provided write endpoint changed from %#x to %#x", writeFlags, flags)
	}
}

func TestPrivatePipesCarryLargeInputAndOutputWithoutTruncation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	process := exec.CommandContext(context.WithoutCancel(ctx), "/bin/sh", "-c", "cat <&3 >/dev/null; cat; exit 7")
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c := &Cmd{ctx: ctx, cmd: process, setup: []byte("{}\n"), abort: make(chan struct{}), done: make(chan struct{})}
	input, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := c.StdoutPipe()
	if err != nil {
		c.closeEnds(true)
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("pipe fixture\n"), 1<<18)
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(input, bytes.NewReader(payload))
		closeErr := input.Close()
		if err == nil {
			err = closeErr
		}
		writeDone <- err
	}()
	got, readErr := io.ReadAll(output)
	writeErr := <-writeDone
	waitErr := c.Wait()
	if readErr != nil || writeErr != nil || !bytes.Equal(got, payload) {
		t.Errorf("large private pipe exchange: got %d of %d bytes, read=%v, write=%v", len(got), len(payload), readErr, writeErr)
	}
	if ended, ok := waitErr.(*ExitError); !ok || ended.Code != 7 {
		t.Errorf("large pipe command ended with %v, want exit7", waitErr)
	}
}
