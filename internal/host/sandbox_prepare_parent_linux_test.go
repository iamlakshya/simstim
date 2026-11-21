//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

// This subprocess uses the actual process-purpose witness, reservation, OP,
// native hello and SCM loan producers. It creates no sandbox or native boot.
func TestSandboxPrepareOriginalParentChildV1(t *testing.T) {
	if os.Getenv("PREPARE_PARENT_CHILD") != "1" {
		return
	}
	if err := InitPlacement(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id, err := HostID()
	if err != nil {
		t.Fatal(err)
	}
	b := &bridge{ctx: ctx, hostID: id}
	args := api.SandboxPrepareArgsV1{Version: 1, Level: "process"}
	raw, _ := json.Marshal(args)
	witness, err := b.newSandboxPrepareWitnessV1(ctx, proto.Request{ID: 1, Op: "sandbox.prepare", Args: raw}, raw, args)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	m, err := buildSandboxPrepareManifestV1(reservation.id, "/usr/bin/true", witness)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindAndSaveSandboxPrepareV1(operation, reservation, m, witness); err != nil {
		t.Fatal(err)
	}
	notify := os.NewFile(3, "original-parent-reservation")
	if _, err := fmt.Fprintln(notify, reservation.id); err != nil {
		t.Fatal(err)
	}
	_ = notify.Close()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Net: "unix", Name: os.Getenv("PREPARE_PARENT_ADDRESS")})
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewNativeStartupLineV1(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	if err := line.WriteNativeHelloV1(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: NativeStartupRoleV1}); err != nil {
		t.Fatal(err)
	}
	frame, err := line.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	var hello proto.Hello
	if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &hello) != nil {
		t.Fatal("the original challenge changed")
	}
	parent, err := witness.originalParentComparisonV1(operation)
	if err != nil {
		t.Fatal(err)
	}
	loan, _ := json.Marshal(startupLoanV1{Version: 1, Primary: reservation.id, Nonce: hello.StartupNonce,
		Purpose: startupLoanPurposeSandboxPrepareV1, PrepareParent: &parent})
	request, _ := json.Marshal(proto.Request{ID: 1, Op: startupLoanOperationV1, Args: loan})
	file, err := operation.native.duplicateLockedFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sendStartupAncillary(conn, proto.TRequest, request, file); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	// The original OP, reservation and selected native parent stay live until
	// this SAME child is killed by its test owner. No path can replace them.
	frame, err = line.ReadFrame()
	mode := os.Getenv("PREPARE_PARENT_MODE")
	if mode == "" {
		return
	}
	var response proto.Response
	if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != 1 || !response.OK {
		t.Fatal("original loan was not acknowledged", err)
	}
	if mode == "eof" {
		_ = line.Close()
		time.Sleep(time.Minute) // The owning test kills this SAME live parent.
		return
	}
	if err := line.WriteFrame(proto.TRequest, proto.Request{ID: 3, Op: StartupReadyOperationV1}); err != nil {
		t.Fatal(err)
	}
	if err := line.WriteFrame(proto.TRequest, proto.Request{ID: 4, Op: "cancel_start"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint64{3, 4} {
		frame, err := line.ReadFrame()
		if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id ||
			id == 3 && response.Error == nil || id == 4 && (!response.OK || response.Error != nil) {
			t.Fatal("queued readiness did not settle before original cleanup ACK", id, err)
		}
		response = proto.Response{}
	}
}

type prepareParentFixtureV1 struct {
	cleanup   *SandboxPrepareParentCleanupV1
	operation *SessionOperation
	holder    *OwnedSupervisorLease
	command   *exec.Cmd
	line      *StartupLineV1
	stopped   bool
}

func actualPrepareParentFixtureV1(t *testing.T, modes ...string) *prepareParentFixtureV1 {
	t.Helper()
	setupIsolatedPrepareHost(t)
	address := "@prepare-parent-" + rand.Text()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: address})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	deadline := time.Now().Add(10 * time.Second)
	_ = listener.SetDeadline(deadline)
	raw, err := listener.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err := raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }); err != nil || optionErr != nil {
		t.Fatal(errors.Join(err, optionErr))
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestSandboxPrepareOriginalParentChildV1$")
	mode := ""
	if len(modes) != 0 {
		mode = modes[0]
	}
	command.Env = append(os.Environ(), "PREPARE_PARENT_CHILD=1", "PREPARE_PARENT_ADDRESS="+address, "PREPARE_PARENT_MODE="+mode)
	command.ExtraFiles = []*os.File{writer}
	command.Stdout, command.Stderr = io.Discard, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	fixture := &prepareParentFixtureV1{command: command}
	t.Cleanup(func() {
		if !fixture.stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		if fixture.cleanup != nil {
			_ = fixture.cleanup.Close()
		}
		if fixture.operation != nil {
			fixture.operation.Close()
		}
		if fixture.holder != nil {
			fixture.holder.Close()
		}
		if fixture.line != nil {
			_ = fixture.line.Close()
		}
	})
	_ = reader.SetReadDeadline(deadline)
	var id string
	if _, err := fmt.Fscanln(reader, &id); err != nil {
		t.Fatal(err)
	}
	fixture.holder, err = AcquireOwnedSupervisorLease(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	fixture.line, err = NewNativeStartupLineV1(conn)
	if err != nil {
		t.Fatal(err)
	}
	_ = fixture.line.SetDeadline(deadline)
	frame, err := fixture.line.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.line.AcceptNativeHelloV1(frame); err != nil {
		t.Fatal(err)
	}
	nonce, err := fixture.holder.PrepareStartupHello(t.Context(), fixture.line)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.line.WriteFrame(proto.THello, proto.Hello{StartupNonce: nonce}); err != nil {
		t.Fatal(err)
	}
	frame, err = fixture.line.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	fixture.operation, err = fixture.holder.AcceptStartupLoan(t.Context(), fixture.line, frame)
	if err != nil {
		t.Fatal(err)
	}
	fixture.cleanup, err = fixture.holder.CaptureSandboxPrepareParentCleanupV1(fixture.operation)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if err := fixture.holder.ReplyStartupLoan(fixture.line, 1, fixture.operation); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (f *prepareParentFixtureV1) kill(t *testing.T) {
	t.Helper()
	if err := f.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := f.command.Wait(); !errors.As(err, &exit) {
		t.Fatal("original parent did not retain its actual SIGKILL result", err)
	}
	f.stopped = true
}

func TestSandboxPrepareParentRequiresOriginalNativeExitV1(t *testing.T) {
	fixture := actualPrepareParentFixtureV1(t)
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || removed {
		t.Fatal("a live parent supplied removal permission", removed, err)
	}
	id := fixture.operation.Primary()
	fixture.kill(t)
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || !removed {
		t.Fatal("positive original parent exit did not complete anchored cleanup", removed, err)
	}
	if _, err := os.Lstat(paths.SessionDir(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original metadata remains", err)
	}
}

func TestSandboxPrepareParentPreservesMovedNativeParentV1(t *testing.T) {
	fixture := actualPrepareParentFixtureV1(t)
	parent := paths.SessionsDir()
	moved := parent + "-retained"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent); _ = os.Rename(moved, parent) })
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.kill(t)
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err == nil || removed {
		t.Fatal("a replaced native parent supplied removal permission", removed, err)
	}
	if _, err := os.Stat(filepath.Join(moved, fixture.operation.Primary(), "manifest.json")); err != nil {
		t.Fatal("moved original was changed", err)
	}
}

func TestSandboxPrepareParentRetainsPartialUnlinkAndRejectsCopiedCleanupV1(t *testing.T) {
	fixture := actualPrepareParentFixtureV1(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := fixture.cleanup.base.reservation.dir
	if err := root.Symlink(outside, "foreign"); err != nil {
		t.Fatal(err)
	}
	fixture.kill(t)
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err == nil || removed {
		t.Fatal("a foreign link was removed", removed, err)
	}
	if !fixture.operation.active.Load() || fixture.cleanup.base.claim == "" {
		t.Fatal("failed unlink dropped the borrowed OP or atomic directory claim")
	}
	copied := &SandboxPrepareParentCleanupV1{original: fixture.cleanup, holder: fixture.holder, operation: fixture.operation,
		peer: fixture.cleanup.peer, parentRef: fixture.cleanup.parentRef, base: fixture.cleanup.base}
	if removed, err := copied.RemoveAfterParentExitV1(); err == nil || removed {
		t.Fatal("a copied cleanup renewed the original owner", removed, err)
	}
	if err := root.Remove("foreign"); err != nil {
		t.Fatal(err)
	}
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || !removed {
		t.Fatal("the SAME anchored owner could not finish after the obstruction was removed", removed, err)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep" {
		t.Fatal("outside target changed", err)
	}
}

func TestSandboxPrepareOriginalCancelSettlesQueuedReadyBeforeCleanupACKV1(t *testing.T) {
	fixture := actualPrepareParentFixtureV1(t, "cancel")
	frame, err := fixture.line.ReadFrame()
	var request proto.Request
	if err != nil || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 3 {
		t.Fatal("original readiness request missing", err)
	}
	if err := fixture.holder.RegisterStartupReady(fixture.line, request.ID); err != nil {
		t.Fatal(err)
	}
	frame, err = fixture.line.ReadFrame()
	if err != nil || json.Unmarshal(frame.Payload, &request) != nil || request.ID != 4 || request.Op != "cancel_start" {
		t.Fatal("original cancellation request missing", err)
	}
	if err := fixture.holder.CheckSandboxPrepareCancelV1(fixture.line); err != nil {
		t.Fatal(err)
	}
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || removed {
		t.Fatal("live cancel supplied parent-death permission", removed, err)
	}
	if err := fixture.holder.ReplySandboxPrepareCancelledReadyV1(fixture.line); err != nil {
		t.Fatal(err)
	}
	id := fixture.operation.Primary()
	if err := errors.Join(fixture.cleanup.Close(), fixture.operation.CloseWithError(), fixture.holder.CloseWithError()); err != nil {
		t.Fatal(err)
	}
	if substituted, err := BeginSessionOperation(t.Context(), id, 0); err == nil {
		substituted.Close()
		t.Fatal("closing borrowed OP released the live original parent's description")
	} else {
		var failure *proto.Error
		if !errors.As(err, &failure) || failure.Code != "busy" {
			t.Fatal("the original OP check failed for another reason", err)
		}
	}
	if err := fixture.line.WriteFrame(proto.TResponse, proto.Response{ID: 4, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.command.Wait(); err != nil {
		t.Fatal("original parent did not consume its ordered native replies", err)
	}
	fixture.stopped = true
	next, err := BeginSessionOperation(t.Context(), id, 0)
	if err != nil {
		t.Fatal("last original reference did not release its native OP", err)
	}
	next.Close()
}

func TestSandboxPrepareEOFDoesNotReplaceOriginalParentExitV1(t *testing.T) {
	fixture := actualPrepareParentFixtureV1(t, "eof")
	if _, err := fixture.line.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatal("original parent did not close its physical line", err)
	}
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || removed {
		t.Fatal("EOF replaced the original parent's positive native exit", removed, err)
	}
	fixture.kill(t)
	if removed, err := fixture.cleanup.RemoveAfterParentExitV1(); err != nil || !removed {
		t.Fatal("actual original parent death did not settle its retained anchors", removed, err)
	}
}
