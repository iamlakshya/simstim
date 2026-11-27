//go:build !windows

package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func init() {
	role := os.Getenv("AGENT_STDIO_TEST_ROLE")
	if role == "" {
		return
	}
	if err := prepareAgentProcess(); err != nil {
		os.Exit(104)
	}
	SelfCgroup = func() ([]byte, error) { return []byte("0::/user.slice/user-1000.slice/session-1.scope\n"), nil }
	CgroupNamespaces = func() (string, string, error) { return "cgroup:[1]", "cgroup:[1]", nil }
	selfCgroupRequired = true
	if role != "" {
		// Inspect numeric inherited descriptors without File.Fd or flag
		// mutation. This proof must actually exercise blocking raw I/O.
		for _, fd := range []uintptr{0, 1, 2} {
			flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if err != nil || flags&unix.O_NONBLOCK != 0 {
				os.Exit(103)
			}
		}
	}
	switch role {
	case "entry":
		os.Exit(RunAgentBridgeProcess(os.Stdin, os.Stdout))
	case "abort":
		os.Exit(agentProcessAbortChild())
	case "library":
		if RunAgentBridge(os.Stdin, os.Stdout) == nil {
			os.Exit(105)
		}
		os.Exit(0)
	}
	os.Exit(104)
}

type agentStdioChild struct {
	cmd    *exec.Cmd
	input  *os.File
	output *os.File
	status *os.File
	done   chan error
	stderr bytes.Buffer
}

func startAgentStdioChild(t *testing.T, role string, fullOutput bool) *agentStdioChild {
	t.Helper()
	agentBridgeEnv(t)
	input, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	receive, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	status, statusWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if fullOutput {
		// This deadline belongs to the test's OWN pipe. Passing the write
		// handle through exec makes the child's inherited file blocking.
		if err := output.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		n, err := output.Write(make([]byte, 1<<20))
		if n == 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("could not fill the owned output pipe: bytes=%d error=%v", n, err)
		}
	}
	child := &agentStdioChild{input: send, output: receive, status: status, done: make(chan error, 1)}
	child.cmd = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	child.cmd.Env = append(os.Environ(), "AGENT_STDIO_TEST_ROLE="+role)
	child.cmd.Stdin, child.cmd.Stdout, child.cmd.Stderr = input, output, &child.stderr
	child.cmd.ExtraFiles = []*os.File{statusWriter}
	if err := child.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = input.Close()
	_ = output.Close()
	_ = statusWriter.Close()
	go func() { child.done <- child.cmd.Wait() }()
	t.Cleanup(func() {
		_ = send.Close()
		_ = receive.Close()
		_ = status.Close()
		// Only this unreaped owned subprocess can be killed. No stored PID
		// is looked up or signalled, even when a regression stalls it.
		_ = child.cmd.Process.Kill()
	})
	return child
}

func (c *agentStdioChild) wait(t *testing.T, want int) {
	t.Helper()
	select {
	case err := <-c.done:
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != want {
			t.Fatalf("owned helper exit=%d want=%d stderr=%q", code, want, c.stderr.String())
		}
	case <-time.After(agentbridge.HandshakeTimeout + 5*time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
		t.Fatal("owned helper waited forever on inherited stdio")
	}
}

func sendAgentIdentity(t *testing.T, child *agentStdioChild) {
	t.Helper()
	w := proto.NewWriter(child.input)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: agentbridge.Role}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentProcessNoPeerInputExitsWithItsRawStdinStillOpen(t *testing.T) {
	child := startAgentStdioChild(t, "entry", false)
	child.wait(t, agentbridge.ProcessExitRefused)
}

func TestAgentLibraryRejectsRawInheritedBlockingFilesBeforeInput(t *testing.T) {
	child := startAgentStdioChild(t, "library", false)
	child.wait(t, 0)
}

func TestAgentProcessNoOutputDrainExitsWithRawWriteInFlight(t *testing.T) {
	child := startAgentStdioChild(t, "entry", true)
	sendAgentIdentity(t, child)
	// Do not drain or close stdout or stdin to rescue the implementation.
	child.wait(t, agentbridge.ProcessExitRefused)
}

func TestAgentProcessBufferedIdentityRetainsTheFollowingLinkHello(t *testing.T) {
	child := startAgentStdioChild(t, "entry", false)
	var batch bytes.Buffer
	w := proto.NewWriter(&batch)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: agentbridge.Role}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := child.input.Write(batch.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := child.output.SetReadDeadline(time.Now().Add(agentbridge.HandshakeTimeout)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(child.output)
	for range 2 {
		frame, err := proto.ReadLimit(reader, agentbridge.MaxIdentityHello)
		if err != nil || frame.Type != proto.THello {
			t.Fatalf("buffered link hello was lost: type=%v error=%v", frame.Type, err)
		}
	}
	_ = child.input.Close()
	child.wait(t, 0)
}

func TestAgentProcessAbortJoinsUnixWorkWithoutRawOutputDrain(t *testing.T) {
	child := startAgentStdioChild(t, "abort", true)
	w := proto.NewWriter(child.input)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor"}); err != nil {
		t.Fatal(err)
	}
	if err := child.status.SetReadDeadline(time.Now().Add(agentbridge.HandshakeTimeout)); err != nil {
		t.Fatal(err)
	}
	var status [1]byte
	if _, err := io.ReadFull(child.status, status[:]); err != nil || status[0] != 1 {
		t.Fatalf("owned Unix work was not revoked before process exit: %v", err)
	}
	child.wait(t, 0)
}

// The child-only path reports after its interruptible work has ended and
// immediately exits. It never continues a host library after raw I/O stop.
func agentProcessAbortChild() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return agentProcessAbortUnix(ctx, cancel, filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "owned-agent-stdio.sock"))
}
