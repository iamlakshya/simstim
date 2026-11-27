//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

// This raw relay owns only its actual current public code observation. It
// receives no private FD, OP, placement source or stopped subject.
type purgeRelayImageV3 struct {
	kernel, readable *os.File
	mapped           os.FileInfo
}

func captureOwnPurgeRelayImageV3() (result *purgeRelayImageV3, returned error) {
	if err := secretmem.Protect(); err != nil {
		return nil, err
	}
	state, err := secretmem.Inspect()
	if err != nil || state.Dumpable == nil || *state.Dumpable != 0 || state.CoreHard != 0 {
		return nil, errors.Join(placementError("the raw purge relay has no actual protected current process"), err)
	}
	owner := &purgeRelayImageV3{}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	owner.kernel = os.NewFile(uintptr(fd), "actual-current-purge-relay-kernel-image")
	owner.mapped, err = owner.kernel.Stat()
	if err != nil {
		return nil, err
	}
	owner.readable, err = os.Open("/proc/self/exe")
	if err != nil {
		return nil, UserError("purge_relay", "the raw relay cannot retain a readable descriptor for its actual mapped code", "Use the matching saved removal payload. Keep the host saved; do not reinstall a helper to reconstruct expired custody.")
	}
	if err := owner.Check(context.Background()); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *purgeRelayImageV3) Check(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.kernel == nil || owner.readable == nil || owner.mapped == nil {
		return placementError("the actual raw relay image is absent")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	current := os.NewFile(uintptr(fd), "current-purge-relay-kernel-mapping")
	actual, err := current.Stat()
	closed := current.Close()
	readable, readErr := owner.readable.Stat()
	if err != nil || closed != nil || readErr != nil || !os.SameFile(actual, owner.mapped) || !os.SameFile(readable, owner.mapped) {
		return errors.Join(placementError("the actual raw relay changed its current native mapping or readable code"), err, closed, readErr)
	}
	return checkPurgePublicCodeBytesV3(ctx, owner.readable, owner.readable, owner.mapped)
}

func (owner *purgeRelayImageV3) writeHelloV3(ctx context.Context, line *StartupLineV1) (returned error) {
	if err := owner.Check(ctx); err != nil {
		return err
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return placementError("the actual raw relay has no native target stream")
	}
	identity := &nativeHelperIdentityV1{executable: owner.readable}
	var err error
	identity.user, err = os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, identity.user.Close()) }()
	identity.cgroup, err = os.Open("/proc/self/ns/cgroup")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, identity.cgroup.Close()) }()
	if err := identity.Check(owner.mapped); err != nil {
		return err
	}
	raw, err := json.Marshal(proto.Hello{Role: "purge-data-relay-v3"})
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	return proto.SendUnixFrameV1(connection, proto.THello, raw, []*os.File{owner.readable, identity.user, identity.cgroup})
}

func (owner *purgeRelayImageV3) Close() error {
	if owner == nil {
		return nil
	}
	var failure error
	for _, file := range []*os.File{owner.kernel, owner.readable} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

func newRawPurgeRelayImageV3() (*purgeRelayImageV3, error) {
	if err := secretmem.Protect(); err != nil {
		return nil, err
	}
	if len(os.Args) != 2 || os.Args[1] != "purge-relay-v3" {
		return nil, placementError("the raw purge relay has another fixed command")
	}
	return captureOwnPurgeRelayImageV3()
}
