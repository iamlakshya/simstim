//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
)

func (owner *auxiliaryOperationV1) connectAuxiliaryV1(ctx context.Context) error {
	if owner == nil || owner.activation == nil || owner.activation.peer == nil {
		return auxiliaryUnavailableV1("the auxiliary connector has no original new activation")
	}
	deadline := time.Now().Add(10 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	for {
		if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
			return err
		}
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket(owner.operation.Primary()))
		if err == nil {
			line, err := newAuxiliaryStartupLineV1(connection)
			if err != nil {
				_ = connection.Close()
				return err
			}
			owner.mu.Lock()
			owner.line = line
			owner.mu.Unlock()
			peer, err := peerOf(connection)
			if err != nil {
				return err
			}
			if peer.done != nil {
				defer peer.done()
			}
			if peer.pid != owner.activation.peer.pid || peer.uid != os.Getuid() || peer.alive != nil && !peer.alive() {
				return auxiliaryUnavailableV1("the auxiliary socket creator differs from the original new native helper")
			}
			if err := line.SetDeadline(deadline); err != nil {
				return err
			}
			if err := line.WriteNativeHelloV1(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: NativeStartupRoleV1}); err != nil {
				return err
			}
			frame, err := line.ReadFrame()
			if err != nil {
				return err
			}
			if err := line.AcceptNativeHelloV1(frame); err != nil {
				return err
			}
			if frame.Type != proto.THello || json.Unmarshal(frame.Payload, &owner.hello) != nil || owner.hello.Check(version.Protocol) != nil || owner.hello.Role != "operation-supervisor-v1" || !supply.IsHex64(owner.hello.StartupNonce) {
				return auxiliaryUnavailableV1("the original auxiliary helper returned no closed native challenge")
			}
			if err := line.bindOriginalNativePeerV1(owner.activation.peer); err != nil {
				return err
			}
			return owner.checkAuxiliaryNativeMemoryV1(ctx)
		}
		if !noHolderSocket(err) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func duplicateAuxiliaryHolderV1(ctx context.Context, lease *sessionFileLease) (*os.File, error) {
	if lease == nil {
		return nil, auxiliaryUnavailableV1("the auxiliary loan has no original holder owner")
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.borrowed || lease.name != holderLockFile {
		return nil, auxiliaryUnavailableV1("the auxiliary holder role is not the original owning description")
	}
	if err := lease.checkLocked(ctx); err != nil {
		return nil, err
	}
	descriptor, err := unix.FcntlInt(lease.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), "auxiliary-holder-loan")
	if err := lease.fileRef.CheckFile(lease.root, lease.name, file, 0600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}
