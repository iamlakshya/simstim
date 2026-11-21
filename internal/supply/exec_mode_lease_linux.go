//go:build linux

package supply

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

var ErrExecModePending = errors.New("runtime mode lease still requires its original final-exec or no-future-exec proof")

type execModeStateV1 struct {
	mu                      sync.Mutex
	guard                   *RuntimeModeGuard
	program                 *os.File // borrowed from the SAME VerifiedRuntime owner
	writer                  *os.File // parent-only intent writer, never inherited
	armed, restored, closed bool
	loans                   int
}

func newExecModeLeaseV1(ctx context.Context, guard *RuntimeModeGuard, program *os.File) (*ExecModeLeaseV1, error) {
	if guard == nil || guard.closed || guard.lock == nil || guard.metadata == nil || program == nil {
		return nil, fmt.Errorf("native mode lease lacks its original owned handles")
	}
	if err := guard.Recover(ctx); err != nil {
		return nil, err
	}
	writable, err := modeMountWritable(guard.metadata)
	if err != nil {
		return nil, err
	}
	if !writable {
		return nil, fmt.Errorf("a read-only artifact mount cannot enter a direct mode phase")
	}
	if err := chmodModeArtifact(guard.metadata, 0o555); err != nil {
		return nil, err
	}
	writer, err := cachefs.OpenFile(guard.modes, modeLeaseName(guard.item), os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := guard.intent.Self.CheckCreatedFile(guard.modes, modeLeaseName(guard.item), writer, 0o600); err != nil {
		writer.Close()
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		writer.Close()
		return nil, err
	}
	guard.intent.Nonce, guard.intent.Armed = hex.EncodeToString(nonce[:]), false
	if err := writeExecModeIntent(ctx, guard.intent, guard.modes, guard.item, writer); err != nil {
		writer.Close()
		return nil, err
	}
	lease := &ExecModeLeaseV1{state: &execModeStateV1{guard: guard, program: program, writer: writer}}
	if err := lease.CheckProgramV1(ctx, program); err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
}

// CheckProgramV1 accepts only the exact original VMM capability captured by
// the native store owner. It does not confer helper/keeper/boot authority;
// jail's private attach also binds its actual creation-owned helper chain.
func (lease *ExecModeLeaseV1) CheckProgramV1(ctx context.Context, program *os.File) error {
	if lease == nil || lease.state == nil {
		return fmt.Errorf("native mode source is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.checkProgram(ctx, program)
}

func (state *execModeStateV1) checkProgram(ctx context.Context, program *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.closed || state.guard == nil || state.program == nil || program == nil {
		return fmt.Errorf("native mode source is closed")
	}
	a, err := state.program.Stat()
	if err != nil {
		return err
	}
	b, err := program.Stat()
	if err != nil {
		return err
	}
	mode := os.FileMode(0o555)
	if state.armed && !state.restored {
		mode = 0o111
	}
	if !os.SameFile(a, b) || b.Mode() != mode || b.Size() != state.guard.item.Size {
		return fmt.Errorf("VMM mode phase received another program or mode")
	}
	flags, err := unix.FcntlInt(program.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if flags&unix.O_ACCMODE != unix.O_RDONLY {
		return fmt.Errorf("VMM mode program is not the original read-only capability")
	}
	if err := state.checkHeldIntent(mode); err != nil {
		return err
	}
	got, err := measureFSVerity(program)
	if err != nil {
		return err
	}
	if err := compareVerity(state.guard.item, got); err != nil {
		return err
	}
	return ctx.Err()
}

func (state *execModeStateV1) checkHeldIntent(mode os.FileMode) error {
	guard := state.guard
	if err := guard.intent.Self.CheckCreatedFile(guard.modes, modeLeaseName(guard.item), guard.lock, 0o600); err != nil {
		return err
	}
	if err := guard.intent.ArtifactsRoot.CheckRoot(guard.artifacts); err != nil {
		return err
	}
	if err := guard.intent.ModesRoot.CheckRoot(guard.modes); err != nil {
		return err
	}
	return checkHeldModeArtifact(guard.intent.Artifact, guard.item, guard.metadata, mode)
}

// RetainForJailV1 duplicates the SAME original RO sidecar OFD. It never lends
// the program's writable-mount metadata handle or the parent intent writer.
// The private Cmd issuer retains it across all pre-final helper execs.
func (lease *ExecModeLeaseV1) RetainForJailV1(ctx context.Context) (*os.File, error) {
	if lease == nil || lease.state == nil {
		return nil, fmt.Errorf("native mode source is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkProgram(ctx, state.program); err != nil {
		return nil, err
	}
	if state.armed || state.restored || state.loans != 0 {
		return nil, fmt.Errorf("native mode source was already retained by its one original private Cmd")
	}
	fd, err := unix.FcntlInt(state.guard.lock.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	state.loans++
	return os.NewFile(uintptr(fd), "original-vmm-mode-lease"), nil
}

// ArmV1 is called ONLY by the same private Cmd after its creation-bound all-
// projection proof. No API or wire field can request a mode transition.
// That Cmd releases its original continuation only after this call succeeds.
func (lease *ExecModeLeaseV1) ArmV1(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return fmt.Errorf("native mode source is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkProgram(ctx, state.program); err != nil {
		return err
	}
	if state.armed || state.restored || state.loans == 0 {
		return fmt.Errorf("native mode phase was not retained for its original private Cmd")
	}
	state.guard.intent.Armed = true
	if err := writeExecModeIntent(ctx, state.guard.intent, state.guard.modes, state.guard.item, state.writer); err != nil {
		state.guard.intent.Armed = false
		return err
	}
	// Treat a failed transition as armed/uncertain until the ORIGINAL native
	// no-future-exec path settles it. Never silently select a sealed fallback.
	state.armed = true
	if err := chmodModeArtifact(state.guard.metadata, 0o111); err != nil {
		return err
	}
	if err := state.program.Sync(); err != nil {
		return err
	}
	return state.checkProgram(ctx, state.program)
}

// RestoreFinalV1 is invoked only by the closed microvm phase after its actual
// final creation/code/chain/adoption proof plus armed sentinel and EOF, OR the
// original whole-source physical no-future-exec proof. EOF or Cmd.Start alone
// does not authorise this call. The original parent lease remains held on error.
func (lease *ExecModeLeaseV1) RestoreFinalV1(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return fmt.Errorf("native mode source is missing")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return fmt.Errorf("native mode source is closed")
	}
	if state.restored {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := state.guard.metadata.Stat()
	if err != nil {
		return err
	}
	if info.Mode() != 0o111 && info.Mode() != 0o555 {
		return fmt.Errorf("final mode restoration lost its original role")
	}
	if err := state.checkHeldIntent(info.Mode()); err != nil {
		return err
	}
	if err := chmodModeArtifact(state.guard.metadata, 0o555); err != nil {
		return err
	}
	if err := state.program.Sync(); err != nil {
		return err
	}
	state.guard.intent.Armed = false
	if err := writeExecModeIntent(ctx, state.guard.intent, state.guard.modes, state.guard.item, state.writer); err != nil {
		return err
	}
	state.restored = true
	// End the actual mode fence NOW, never at VM shutdown. Program ownership
	// remains with VerifiedRuntime through its existing physical boot lifetime.
	err = errors.Join(state.writer.Close(), state.guard.Close())
	state.writer = nil
	return err
}

func (lease *ExecModeLeaseV1) Close() error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	if state.armed && !state.restored {
		return ErrExecModePending
	}
	state.closed = true
	var err error
	if state.writer != nil {
		err = state.writer.Close()
		state.writer = nil
	}
	if state.guard != nil {
		err = errors.Join(err, state.guard.Close())
	}
	return err
}
