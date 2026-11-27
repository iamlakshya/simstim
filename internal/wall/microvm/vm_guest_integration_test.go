//go:build linux && chvintegration

package microvm_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

type vmStageResult struct {
	exit  int
	ready string
	mem   *proto.GuestMemoryV1
}

// runVMStage uses the real admitted driver/link/prepare/PTY path. No test
// spawner, host shell fallback or synthetic v2 readiness can satisfy it.
func runVMStage(ctx context.Context, peer *link.Session, setup proto.CtlVMPrepareArgs, script string) (vmStageResult, error) {
	result := vmStageResult{exit: -1}
	ctl, err := peer.Open(ctx, link.KindCtl, nil)
	if err != nil {
		return result, err
	}
	defer ctl.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = ctl.SetDeadline(deadline)
	}
	writer := proto.NewWriter(ctl)
	reader := bufio.NewReader(ctl)
	encoded, err := json.Marshal(setup)
	if err != nil {
		return result, err
	}
	if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: proto.CtlVMPrepare, Args: encoded}); err != nil {
		return result, err
	}
	started := false
	for {
		frame, err := proto.Read(reader)
		if err != nil {
			// State-ready is followed by actual guest filesystem cleanup/reboot;
			// the caller additionally requires owned CleanExit before using nonce.
			if result.exit >= 0 && (err == io.EOF || strings.Contains(err.Error(), "EOF")) {
				return result, nil
			}
			return result, err
		}
		if frame.Type == proto.TResponse {
			var response proto.Response
			if err := json.Unmarshal(frame.Payload, &response); err != nil {
				return result, err
			}
			if !response.OK {
				return result, fmt.Errorf("VM fixture control refused: %v", response.Error)
			}
			if response.ID == 1 {
				var applied proto.CtlVMPrepareResult
				if json.Unmarshal(response.Result, &applied) != nil || applied.GuestABI != proto.GuestOwnedPTYV2 {
					return result, fmt.Errorf("actual init-owned v2 prepare missing")
				}
				args := proto.CtlStartArgs{Session: setup.Session, RunToken: "fixture-owned-run", DNS: proto.DNSSynthetic, Agent: "shell", Argv: []string{"/bin/sh", "-ceu", script}, Env: []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "LANG=C"}, Cwd: "/work", Cols: 120, Rows: 40, StateFiles: setup.StateFiles, CaptureNonce: setup.CaptureNonce}
				encoded, err := json.Marshal(args)
				if err != nil {
					return result, err
				}
				if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: proto.CtlStart, Args: encoded}); err != nil {
					return result, err
				}
			}
			if response.ID == 2 {
				var actual proto.CtlStartResult
				if json.Unmarshal(response.Result, &actual) != nil || actual.GuestABI != proto.GuestOwnedPTYV2 || actual.AgentPID <= 1 {
					return result, fmt.Errorf("real owned1000child missing")
				}
				started = true
			}
		}
		if frame.Type == proto.TEvent {
			var event proto.CtlEvent
			if err := json.Unmarshal(frame.Payload, &event); err != nil {
				return result, err
			}
			switch event.Event {
			case proto.EventMem:
				result.mem = event.GuestMemoryV1
			case proto.EventEnded:
				if event.Session == setup.Session && event.ExitCode != nil {
					result.exit = *event.ExitCode
				}
			case proto.EventStateReady:
				if event.Session == setup.Session {
					result.ready = event.CaptureNonce
				}
			}
		}
		if started && result.exit >= 0 && len(setup.StateFiles) == 0 {
			return result, nil
		}
	}
}

func requireOwnedVMClean(t *testing.T, ctx context.Context, sandbox wall.Sandbox) {
	t.Helper()
	done := make(chan wall.Exit, 1)
	go func() { done <- sandbox.Wait() }()
	var exit wall.Exit
	select {
	case exit = <-done:
	case <-ctx.Done():
		_ = sandbox.Kill()
		t.Fatal("owned VM did not complete clean shutdown within fixture deadline")
	}
	if exit.Err != nil || exit.Code != 0 || exit.Signal != "" {
		t.Fatal("actual VMM cleanup was not clean", exit)
	}
	evidence, ok := sandbox.(interface{ CleanExit() bool })
	if !ok || !evidence.CleanExit() {
		t.Fatal("guest claims did not establish owned clean VMM evidence")
	}
}

func TestNativeVMHomeResetWorkHistoryAndPendingFileGeneration(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	sandbox, peer := bootNative(t, ctx, spec, false)
	defer sandbox.Destroy()
	nonce := strings.Repeat("a", 32)
	setup := proto.CtlVMPrepareArgs{Session: spec.ID, VM: proto.VMStartV1{Version: 1, System: "reset", StateDirs: []string{".claude/projects"}}, StateFiles: []string{".claude.json"}, CaptureNonce: nonce}
	first, err := runVMStage(ctx, peer, setup, `test "$(id -u)" = 1000
test "$(id -g)" = 1000
printf work > /work/persist
printf ephemeral > "$HOME/.bashrc"
printf history > "$HOME/.claude/projects/history"
printf accepted > "$HOME/.claude.json"`)
	if err != nil || first.exit != 0 || first.ready != nonce {
		t.Fatal("real first capture failed", first, err)
	}
	requireOwnedVMClean(t, ctx, sandbox)
	peer.Close()
	second, peer := bootNative(t, ctx, spec, true)
	defer second.Destroy()
	// This test fixture acts as the protected publisher ONLY after the above
	// actual owned clean exit, never because state_ready arrived on its own.
	setup.VM.RestoreCaptureNonce = nonce
	setup.CaptureNonce = strings.Repeat("b", 32)
	result, err := runVMStage(ctx, peer, setup, `test ! -e "$HOME/.bashrc"
test "$(cat /work/persist)" = work
test "$(cat "$HOME/.claude/projects/history")" = history
test "$(cat "$HOME/.claude.json")" = accepted`)
	if err != nil || result.exit != 0 {
		t.Fatal("reset/persistence contract failed", result, err)
	}
	requireOwnedVMClean(t, ctx, second)
	peer.Close()
}

func TestNativeVMDefaultPrivilegeAndDeviceBoundary(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sandbox, peer := bootNative(t, ctx, spec, false)
	defer sandbox.Destroy()
	result, err := runVMStage(ctx, peer, proto.CtlVMPrepareArgs{Session: spec.ID, VM: proto.VMStartV1{Version: 1, System: "reset"}}, `test "$(id -u)" = 1000
for p in /dev/kvm /dev/vda /dev/vdb /dev/vdc /dev/vdd /dev/fuse; do test ! -e "$p"; done
for p in /.tools /.persist /run/*/members/*/holder.json; do test ! -r "$p"; done
if command -v sudo >/dev/null; then ! sudo -n true; fi
test ! -S /var/run/docker.sock
# An installed ptrace probe must exercise the actual syscall against900.
# It is a test-only binary on the admitted agents fixture, not a fake result.
/opt/agents/vm-boundary-probe --require-uid=1000 --holder-uid=900 --require-ptrace-denied --require-host-sockets-absent`)
	if err != nil || result.exit != 0 {
		t.Fatal("guest identity/device/ptrace boundary failed", result, err)
	}
	peer.Close()
	_ = sandbox.Stop(ctx, time.Second)
	sandbox.Wait()
}

func TestNativeVMDockerUsesActualImageDaemonAndGuestSocket(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sandbox, peer := bootNative(t, ctx, spec, false)
	defer sandbox.Destroy()
	result, err := runVMStage(ctx, peer, proto.CtlVMPrepareArgs{Session: spec.ID, VM: proto.VMStartV1{Version: 1, System: "reset", Docker: true}}, `test "$(id -u)" = 1000
id -G | tr ' ' '\n' | grep -x 2000
test -S /run/`+proto.Magic+`/docker.sock
DOCKER_HOST=unix:///run/`+proto.Magic+`/docker.sock docker info --format '{{.ServerVersion}}'
test -c /dev/fuse`)
	if err != nil || result.exit != 0 {
		t.Fatal("actual Docker guest setup failed (fixture image must contain dockerd/docker)", result, err)
	}
	peer.Close()
	_ = sandbox.Stop(ctx, time.Second)
	sandbox.Wait()
}

func TestNativeVMSystemKeepUsesActualSudoAndFreshHome(t *testing.T) {
	spec := admittedPlan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sandbox, peer := bootNative(t, ctx, spec, false)
	defer sandbox.Destroy()
	setup := proto.CtlVMPrepareArgs{Session: spec.ID, VM: proto.VMStartV1{Version: 1, System: "keep", Sudo: true}}
	first, err := runVMStage(ctx, peer, setup, `test "$(id -u)" = 1000
sudo -n /bin/sh -ceu 'test "$(id -u)" = 0; printf retained > /etc/vm-system-kept'
printf ephemeral > "$HOME/.bashrc"`)
	if err != nil || first.exit != 0 {
		t.Fatal("real sudo fixture setup failed", first, err)
	}
	requireOwnedVMClean(t, ctx, sandbox)
	peer.Close()
	second, peer := bootNative(t, ctx, spec, true)
	defer second.Destroy()
	result, err := runVMStage(ctx, peer, setup, `test "$(cat /etc/vm-system-kept)" = retained
test ! -e "$HOME/.bashrc"`)
	if err != nil || result.exit != 0 {
		t.Fatal("system keep changed fresh-home contract", result, err)
	}
	requireOwnedVMClean(t, ctx, second)
	peer.Close()
}
