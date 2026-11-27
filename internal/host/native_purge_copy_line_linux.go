//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

const nativePurgeAttemptRoleV2 = "purge-attempt-v2"

// This is ONLY the SAME actual creator-side admission of its fixed child.
// It cannot admit a copy to an unrelated holder or authenticate a parent from
// public code/PIDFD bags. Those cross-hop producers remain independently open.
func (line *StartupLineV1) acceptOriginalPurgeAttemptHelloV2(ctx context.Context, frame proto.Frame, owner *purgeAttemptExecV1) (failure error) {
	if line == nil || owner == nil || ctx == nil {
		return placementError("the copy hello has no same actual execution creator")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.line != line || owner.start == nil || owner.start.creator != owner || owner.command == nil || owner.command.Env == nil || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD {
		return placementError("the copy hello did not select its original real Start source")
	}
	if err := owner.checkCreatorLockedV1(ctx); err != nil {
		return err
	}
	identity, sender, err := line.takeNativeHelperIdentityFrameV1(frame, proto.THello)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, identity.Close())
		}
	}()
	var hello proto.Hello
	if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Role != nativePurgeAttemptRoleV2 {
		return placementError("the actual copy hello has another closed purpose")
	}
	expected, err := owner.loan.CopiedCode.Stat()
	if err != nil {
		return err
	}
	if err := identity.Check(expected); err != nil {
		return err
	}
	peer, err := pinUnverifiedNativeHelperPeerV1(line.conn)
	if err != nil {
		return err
	}
	adopted := false
	defer func() {
		if !adopted {
			failure = errors.Join(failure, peer.Close())
		}
	}()
	if peer.pid != owner.process.Pid || peer.uid != owner.peer.uid {
		return placementError("the native copy socket belongs to another actual process")
	}
	if err := errors.Join(peer.checkNativeSender(sender), owner.peer.checkNativeSender(sender)); err != nil {
		return err
	}
	identity.expected = expected
	peer.identity = identity
	// Actual current cgroup placement and native namespace checks remain strict.
	if err := peer.Check(); err != nil {
		return err
	}
	if err := owner.checkCreatorLockedV1(ctx); err != nil {
		return err
	}
	line.mu.Lock()
	if line.closed || line.nativePeer != nil {
		line.mu.Unlock()
		return placementError("the original copy line ended or replaced its accepted native peer")
	}
	line.nativePeer = peer
	line.mu.Unlock()
	adopted = true
	complete = true
	if err := line.bindOriginalNativePeerV1(owner.peer); err != nil {
		line.mu.Lock()
		if line.nativePeer == peer {
			line.nativePeer = nil
		}
		line.mu.Unlock()
		return errors.Join(err, peer.Close())
	}
	return owner.checkCreatorLockedV1(ctx)
}

// This writer emits ONLY its own current mapped-code/native-namespace public
// identity. It authenticates no parent and grants no transaction/private OP.
func (line *StartupLineV1) writePurgeAttemptNativeHelloV2(ctx context.Context, image *purgeAttemptImageV2) (failure error) {
	if line == nil || image == nil || ctx == nil {
		return placementError("the copy hello has no actual raw image or lifetime")
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	image.mu.Lock()
	if err := image.checkLockedV2(ctx); err != nil {
		image.mu.Unlock()
		return err
	}
	fd, err := unix.FcntlInt(image.copied.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	expected := image.mapped
	image.mu.Unlock()
	if err != nil {
		return err
	}
	identity := &nativeHelperIdentityV1{executable: os.NewFile(uintptr(fd), "actual-current-readable-copy-code")}
	defer func() {
		for _, file := range []*os.File{identity.executable, identity.user, identity.cgroup} {
			if file != nil {
				failure = errors.Join(failure, file.Close())
			}
		}
	}()
	identity.user, err = os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	identity.cgroup, err = os.Open("/proc/self/ns/cgroup")
	if err != nil {
		return err
	}
	if err := identity.Check(expected); err != nil {
		return err
	}
	if err := image.Check(ctx); err != nil {
		return err
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return placementError("the actual copy hello has no native Unix stream")
	}
	data, err := json.Marshal(proto.Hello{Role: nativePurgeAttemptRoleV2})
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return proto.SendUnixFrameV1(connection, proto.THello, data, []*os.File{identity.executable, identity.user, identity.cgroup})
}
