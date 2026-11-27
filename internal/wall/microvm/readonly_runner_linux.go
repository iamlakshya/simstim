//go:build linux

package microvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/proto"
	fc "github.com/iamlakshya/simstim/internal/wall/firecracker/backend"
	"golang.org/x/sys/unix"
)

// B is the existing C-created control helper, with its own collapsed UID/GID1
// and caps0. Its entry has already established original native provenance and
// TSYNC no-exec. It never selects, creates, launches or execs a VMM.
func runReadonlyVMControlV1(stage *jail.VMMReadonlyControlStageV1) (returned error) {
	defer func() { returned = errors.Join(returned, stage.Close()) }()
	rawPlan, err := stage.PlanV1()
	if err != nil {
		return err
	}
	var plan runnerPlan
	if err := decodeHelper(bytes.NewReader(rawPlan), &plan); err != nil {
		return err
	}
	if plan.Version != 3 || plan.UIDMode != "subuid" && plan.UIDMode != "login" {
		return fmt.Errorf("the original read-only control plan has another purpose")
	}
	if err := validateVMSpec(plan.Spec); err != nil {
		return err
	}
	kind, err := backendForSpec(plan.Spec)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	control, err := stage.ControlV1()
	if err != nil {
		return err
	}
	server := newRunnerServer(control)
	raw, err := inheritedConnection(9)
	if err != nil {
		return err
	}
	defer raw.Close()
	root, err := cachefs.OpenPrivateRoot(vmDataDir(plan.Spec), false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	disk, err := cachefs.OpenFile(root, SessionDiskName, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if disk != nil {
			returned = errors.Join(returned, disk.Close())
		}
	}()
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, parent.Close()) }()
	var inheritedParent, actualParent unix.Stat_t
	if err := errors.Join(unix.Fstat(11, &inheritedParent), unix.Fstat(int(parent.Fd()), &actualParent)); err != nil {
		return err
	}
	if inheritedParent.Dev != actualParent.Dev || inheritedParent.Ino != actualParent.Ino || actualParent.Mode&unix.S_IFMT != unix.S_IFDIR || actualParent.Mode&0o7777 != 0o700 || actualParent.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("the original subordinate disk root differs from its inherited native anchor")
	}
	server.readonlyDisk = &readonlyControlDiskV2{root: root, disk: disk, parent: parent, session: plan.Spec.ID, driver: kind.name(), bytes: plan.Spec.DiskMax}
	info, err := disk.Stat()
	if err != nil || info.Size() != plan.Spec.DiskMax {
		return errors.Join(err, fmt.Errorf("the original subordinate session disk changed size"))
	}
	var header [4]byte
	if _, err := disk.ReadAt(header[:], 0); err != nil {
		return err
	}
	if bytes.Equal(header[:], []byte{'Q', 'F', 'I', 0xfb}) {
		return fmt.Errorf("the original session disk is not raw")
	}
	api, vsock := vmSocketPaths(vmDataDir(plan.Spec))
	for _, path := range []string{api, vsock} {
		if err := removeOwnSocket(path); err != nil {
			return err
		}
	}
	if err := stage.ReleaseNoExecV1(); err != nil {
		return err
	}
	message, err := json.Marshal(runnerMessage{Kind: "mode_init_disk_v2", Session: plan.Spec.ID, Driver: kind.name()})
	if err != nil {
		return err
	}
	server.mu.Lock()
	err = proto.SendUnixControlV1(control, append(message, '\n'), []*os.File{disk, parent})
	server.mu.Unlock()
	if err != nil {
		return err
	}
	if err := stage.WaitExecV1(ctx, plan.BootUntil); err != nil {
		return err
	}
	parent, pid, err := stage.RetainInitV1()
	if err != nil {
		return err
	}
	fd, duplicateErr := unix.FcntlInt(parent.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	closeErr := parent.Close()
	if duplicateErr != nil || closeErr != nil {
		if fd >= 0 {
			unix.Close(fd)
		}
		return errors.Join(duplicateErr, closeErr)
	}
	owner := &ownedVMMEndpoint{fd: fd, pid: pid, uid: uint32(os.Getuid())}
	defer func() { returned = errors.Join(returned, owner.close()) }()
	connectCtx, cancelConnect := context.WithDeadline(ctx, plan.BootUntil)
	defer cancelConnect()
	guest, err := connectGuest(connectCtx, vsock, owner.dial, pauseVMConnect)
	if err != nil {
		return err
	}
	defer guest.Close()
	var firecracker *fc.Client
	if kind == firecrackerBackend {
		connection, err := owner.connectAPI(connectCtx, api)
		if err != nil {
			return err
		}
		firecracker, err = fc.NewClient(connection)
		if err != nil {
			connection.Close()
			return err
		}
		defer firecracker.Close()
	}
	if err := server.sendNativeReady(plan.Spec.ID, kind.name(), owner, guest); err != nil {
		return err
	}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); server.serve(ctx, api, owner, firecracker) }()
	defer func() {
		cancel()
		// Interrupt only the reader here. Stage remains the sole owner of
		// its actual control Close; no raw descriptor close is retried.
		interruptErr := control.SetReadDeadline(time.Now())
		<-serveDone
		returned = errors.Join(returned, interruptErr, server.closeCheckpointActorV3())
	}()
	go func() { _, _ = io.Copy(guest, raw); guest.Close() }()
	go func() { _, _ = io.Copy(raw, guest); raw.Close() }()
	// B is not C's parent and never fabricates a child wait/exit result.
	// C's actual PID1 death kills B; S/A retain the real native wait authority.
	for {
		if err := owner.live(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
