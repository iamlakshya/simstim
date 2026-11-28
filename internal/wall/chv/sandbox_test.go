//go:build unix

package chv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
)

func TestCHRemoteWrapperAndSandboxLifecycle(t *testing.T) {
	dir := shortSockDir(t)
	apiSock := filepath.Join(dir, "vm.vmm")
	vsockSock := filepath.Join(dir, "vm.vsock")
	powerFile := filepath.Join(dir, "power.pressed")
	pidFile := filepath.Join(dir, "vmm.pid")

	// Start a fake vsock listener that answers CONNECT 1024.
	ln, err := net.Listen("unix", vsockSock)
	if err != nil {
		t.Fatalf("listen vsock: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 32)
		_, _ = c.Read(buf)
		_, _ = io.WriteString(c, "OK 4242\n")
		// Keep open until closed.
		_, _ = io.Copy(io.Discard, c)
		_ = c.Close()
	}()

	// Fake VMM script: writes PID, prints stderr, and exits cleanly once powerFile exists.
	fakeVMM := filepath.Join(dir, "fake-chv")
	vmmScript := fmt.Sprintf(`#!/bin/sh
echo $$ > %q
echo "vmm booting" >&2
while [ ! -f %q ]; do
  sleep 0.02
done
echo "vmm power off" >&2
exit 0
`, pidFile, powerFile)
	if err := os.WriteFile(fakeVMM, []byte(vmmScript), 0o755); err != nil {
		t.Fatalf("write fake vmm: %v", err)
	}

	// Fake ch-remote script: supports info and power-button.
	fakeRemote := filepath.Join(dir, "fake-ch-remote")
	remoteScript := fmt.Sprintf(`#!/bin/sh
case "$3" in
  info)
    echo '{"state":"Running"}'
    ;;
  power-button)
    touch %q
    ;;
  *)
    echo "bad subcommand $3" >&2
    exit 1
    ;;
esac
`, powerFile)
	if err := os.WriteFile(fakeRemote, []byte(remoteScript), 0o755); err != nil {
		t.Fatalf("write fake ch-remote: %v", err)
	}

	// Create a dummy pre-existing vsock listener AFTER Sandbox removes stale sockets:
	// instead, let the fake VMM script itself not own vsock, so use SkipVsock=true OR
	// test Info/PowerButton + Sandbox Stop directly.
	info, err := Info(context.Background(), fakeRemote, apiSock)
	if err != nil || info.State != "Running" {
		t.Fatalf("Info = (%+v, %v), want Running", info, err)
	}

	sb := NewSandbox(SandboxOptions{
		Config: VMConfig{
			VMMPath:      fakeVMM,
			CHRemotePath: fakeRemote,
			APISocket:    apiSock,
			VsockSocket:  filepath.Join(dir, "vm2.vsock"),
			KernelPath:   "/tmp/vmlinux",
			CPUs:         1,
			MemoryBytes:  512 << 20,
			Tools: DiskVerity{
				Path:       "/tmp/tools.erofs",
				RootHash:   strings.Repeat("a", 64),
				DataBlocks: 16,
			},
		},
		SkipVsock:   true,
		DisableJail: true,
	})

	if _, err := sb.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := sb.Stop(context.Background(), 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	ex := sb.Wait()
	if ex.Code != 0 || ex.Err != nil {
		t.Fatalf("Wait = %+v, want clean exit 0", ex)
	}
	if !strings.Contains(sb.Stderr(), "vmm power off") {
		t.Errorf("Stderr = %q, want vmm power off", sb.Stderr())
	}
	// Kill after clean Stop/Wait must return nil immediately without signaling a reaped PID.
	if err := sb.Kill(); err != nil {
		t.Fatalf("Kill after clean exit: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestSandboxEarlyExitAndKillAfterExit(t *testing.T) {
	dir := shortSockDir(t)
	fakeVMM := filepath.Join(dir, "fail-chv")
	script := "#!/bin/sh\necho 'vmm fatal error' >&2\nexit 2\n"
	if err := os.WriteFile(fakeVMM, []byte(script), 0o755); err != nil {
		t.Fatalf("write failing vmm: %v", err)
	}
	sb := NewSandbox(SandboxOptions{
		Config: VMConfig{
			VMMPath:     fakeVMM,
			APISocket:   filepath.Join(dir, "e.vmm"),
			VsockSocket: filepath.Join(dir, "e.vsock"),
			KernelPath:  "/tmp/vmlinux",
			CPUs:        1,
			MemoryBytes: 512 << 20,
			Tools: DiskVerity{
				Path:       "/tmp/tools.erofs",
				RootHash:   strings.Repeat("b", 64),
				DataBlocks: 16,
			},
		},
		BootTimeout:   2 * time.Second,
		VsockInterval: 10 * time.Millisecond,
		DisableJail:   true,
	})
	conn, err := sb.Start(context.Background())
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "sandbox_boot") {
		t.Fatalf("Start = %v, want sandbox_boot error", err)
	}
	ex := sb.Wait()
	if ex.Code != 2 {
		t.Fatalf("Wait = %+v, want exit code 2", ex)
	}
	if err := sb.Kill(); err != nil {
		t.Fatalf("Kill after early exit: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestTailBufferCapsAtLimit(t *testing.T) {
	tb := newTailBuffer(8)
	_, _ = tb.Write([]byte("12345"))
	_, _ = tb.Write([]byte("67890"))
	if got := tb.String(); got != "34567890" {
		t.Fatalf("tail = %q, want 34567890", got)
	}
	_, _ = tb.Write([]byte("abcdefghijkl"))
	if got := tb.String(); got != "efghijkl" {
		t.Fatalf("tail overflow = %q, want efghijkl", got)
	}
}

func TestSandboxJailsWhenBwrapPathEmpty(t *testing.T) {
	dir := shortSockDir(t)
	var gotSpec jail.Spec
	called := false
	orig := jailCommand
	jailCommand = func(_ context.Context, s jail.Spec) (*exec.Cmd, error) {
		called = true
		gotSpec = s
		return nil, errors.New("landlock ABI below 3")
	}
	t.Cleanup(func() { jailCommand = orig })

	sb := NewSandbox(SandboxOptions{
		Config: VMConfig{
			VMMPath:     "/opt/bin/cloud-hypervisor",
			APISocket:   filepath.Join(dir, "vm.vmm"),
			VsockSocket: filepath.Join(dir, "vm.vsock"),
			KernelPath:  "/opt/kernel/vmlinux",
			CPUs:        1,
			MemoryBytes: 512 << 20,
			Tools: DiskVerity{
				Path:       "/opt/tools/tools.erofs",
				RootHash:   strings.Repeat("c", 64),
				DataBlocks: 16,
			},
		},
		// BwrapPath intentionally empty and DisableJail false: Start must still
		// invoke internal/jail (for fallback or fail-closed refusal), never run unjailed.
		SkipVsock: true,
	})

	_, err := sb.Start(context.Background())
	if !called {
		t.Fatal("Start skipped jailCommand when BwrapPath was empty")
	}
	if err == nil || !strings.Contains(err.Error(), "prepare VMM jail") {
		t.Fatalf("Start err = %v, want sandbox_boot: prepare VMM jail refusal", err)
	}
	if gotSpec.BwrapPath != "" || gotSpec.Seccomp != jail.ProfileVMM || gotSpec.Net != jail.NetNone {
		t.Fatalf("got jail.Spec = %+v, want BwrapPath=\"\" ProfileVMM NetNone", gotSpec)
	}
}
