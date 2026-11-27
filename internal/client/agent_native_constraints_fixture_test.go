//go:build darwin || linux

package client

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type nativeConstraintAgent struct {
	root, socket, add string
	ctx               context.Context
	env               []string
}

type nativeConstraintOutput struct {
	mu   sync.Mutex
	text []byte
}

func (o *nativeConstraintOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.text = append(o.text, p[:min(len(p), (4<<10)-len(o.text))]...)
	return len(p), nil
}

func (o *nativeConstraintOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.text)
}

func newNativeConstraintAgent(t *testing.T) *nativeConstraintAgent {
	t.Helper()
	// Defend the helper too: no process, socket or environment setup may
	// occur accidentally when the explicit native-job admission is absent.
	if os.Getenv("H5_NATIVE_AGENT") != "1" {
		t.Fatal("native-agent fixture requires H5_NATIVE_AGENT=1")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, "")
		}
	}
	root := nativeLogTestEnv(t)
	for _, variable := range []string{"DISPLAY", "SSH_SK_PROVIDER", "SSH_PKCS11_HELPER", "SSH_SK_HELPER"} {
		t.Setenv(variable, "")
	}
	t.Setenv("SSH_ASKPASS_REQUIRE", "never")
	t.Setenv("SSH_ASKPASS", filepath.Join(root, "no-askpass"))
	agentBinary, err := exec.LookPath("ssh-agent")
	if err != nil {
		t.Fatalf("admitted native-agent job requires OpenSSH ssh-agent: %v", err)
	}
	addBinary, err := exec.LookPath("ssh-add")
	if err != nil {
		t.Fatalf("admitted native-agent job requires OpenSSH ssh-add with destination constraints: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	fixture := &nativeConstraintAgent{root: root, socket: filepath.Join(root, "agent.sock"), add: addBinary, ctx: ctx}
	output := &nativeConstraintOutput{}
	command := exec.CommandContext(ctx, agentBinary, "-D", "-a", fixture.socket)
	command.Env = os.Environ() // The isolated fixture environment, before the local socket is installed.
	command.Stdout, command.Stderr = output, output
	command.WaitDelay = 2 * time.Second
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		// This is the retained owned process handle, never an agent PID
		// parsed from environment or from the process's textual output.
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned disposable ssh-agent did not exit within its cleanup bound")
		}
	})
	ready, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if info, err := os.Lstat(fixture.socket); err == nil {
			if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o600 {
				t.Fatalf("owned native-agent endpoint is not a private 0600 Unix socket: %v", info.Mode())
			}
			connection, err := (&net.Dialer{}).DialContext(ready, "unix", fixture.socket)
			if err == nil {
				_ = connection.Close()
				break
			}
		}
		select {
		case <-ready.Done():
			t.Fatalf("owned native-agent socket did not become ready: %v (%s)", ready.Err(), output.String())
		case <-done:
			t.Fatalf("owned native-agent exited before socket readiness: %s", output.String())
		case <-tick.C:
		}
	}
	t.Setenv("SSH_AUTH_SOCK", fixture.socket)
	fixture.env = os.Environ()
	return fixture
}

func (f *nativeConstraintAgent) command(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	output := &nativeConstraintOutput{}
	command := exec.CommandContext(ctx, f.add, args...)
	command.Env = f.env
	command.Stdout, command.Stderr = output, output
	command.WaitDelay = 2 * time.Second
	if err := command.Run(); err != nil {
		t.Fatalf("isolated ssh-add failed (destination constraints require OpenSSH 8.9 or newer): %v (%s)", err, output.String())
	}
}

func (f *nativeConstraintAgent) dial(t *testing.T) net.Conn {
	t.Helper()
	connection, err := (&net.Dialer{}).DialContext(f.ctx, "unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := f.ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	stop := context.AfterFunc(f.ctx, func() { _ = connection.Close() })
	t.Cleanup(func() {
		stop()
		_ = connection.Close()
	})
	return connection
}

type nativeConstraintTrace struct {
	mu             sync.Mutex
	first          []byte
	opcodes        []byte
	prefixAccepted bool
}

func (r *nativeConstraintTrace) request(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.opcodes) == 0 {
		r.first = bytes.Clone(frame)
	}
	r.opcodes = append(r.opcodes, frame[4])
}

func (r *nativeConstraintTrace) reply(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.opcodes) == 1 {
		r.prefixAccepted = len(frame) == 5 && frame[4] == agentFWSuccess
	}
}

func (r *nativeConstraintTrace) require(t *testing.T, prefix []byte, opcodes ...byte) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.prefixAccepted || !bytes.Equal(r.first, prefix) || !bytes.Equal(r.opcodes, opcodes) {
		t.Fatalf("real source did not receive the exact successful prefix and serial operation order: accepted=%v opcodes=%v want=%v", r.prefixAccepted, r.opcodes, opcodes)
	}
}

// This transparent fixture observes complete frames on one fresh source FD.
// It changes neither bytes nor order and never pools or reuses an agent FD.
func (f *nativeConstraintAgent) firewall(t *testing.T, binding *peerAgentBinding) (net.Conn, *nativeConstraintTrace) {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	native := f.dial(t)
	source, observer := net.Pipe()
	remote, firewall := net.Pipe()
	trace := &nativeConstraintTrace{}
	proxyDone, firewallDone := make(chan error, 1), make(chan error, 1)
	go func() {
		defer observer.Close()
		defer native.Close()
		proxyDone <- nativeConstraintProxy(observer, native, trace)
	}()
	go func() { firewallDone <- forwardAgentAuthentication(ctx, firewall, source, binding) }()
	t.Cleanup(func() {
		// Interrupt every owned endpoint before waiting for either worker.
		cancel()
		for _, connection := range []net.Conn{remote, firewall, source, observer, native} {
			_ = connection.Close()
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for _, result := range []<-chan error{firewallDone, proxyDone} {
			select {
			case <-result:
			case <-deadline.C:
				t.Error("owned native-agent firewall fixture did not stop within its cleanup bound")
				return
			}
		}
	})
	deadline, _ := ctx.Deadline()
	if err := remote.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	return remote, trace
}

func nativeConstraintProxy(remote, source net.Conn, trace *nativeConstraintTrace) error {
	for {
		request, err := fwReadFrame(remote)
		if err != nil {
			return err
		}
		trace.request(request)
		if err := fwSendFrame(source, request); err != nil {
			return err
		}
		reply, err := fwReadFrame(source)
		if err != nil {
			return fmt.Errorf("read disposable native-agent reply: %w", err)
		}
		trace.reply(reply)
		if err := fwSendFrame(remote, reply); err != nil {
			return err
		}
	}
}
