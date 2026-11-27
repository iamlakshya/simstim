//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/version"
	"golang.org/x/sys/unix"
)

// Only the raw inherited-FD entry constructs this own-current-image producer.
// Neither public code DATA nor the parent handle grants a root loan.
type ordinaryPurgeGitImageV3 struct {
	kernel, code, parent *os.File
	mapped               os.FileInfo
}

// RunOrdinaryPurgeGitWorkerProcessV3 enters the fixed reader before normal
// helper initialisation. The dispatcher must immediately exit with its result.
func RunOrdinaryPurgeGitWorkerProcessV3() int {
	image, err := adoptRawOrdinaryPurgeGitImageV3()
	if err != nil {
		return 1
	}
	handled, runErr := runOrdinaryGitWorkerEntryV3([]string{gitOpPurgeInspectV1}, image)
	closeErr := image.Close()
	if !handled || errors.Join(runErr, closeErr) != nil {
		return 1
	}
	return 0
}

func adoptRawOrdinaryPurgeGitImageV3() (result *ordinaryPurgeGitImageV3, failure error) {
	if err := secretmem.Protect(); err != nil {
		return nil, err
	}
	state, err := secretmem.Inspect()
	if err != nil || state.Dumpable == nil || *state.Dumpable != 0 || state.CoreHard != 0 {
		return nil, errors.Join(sessionLeaseError("the raw ordinary Git reader has no native process protection"), err)
	}
	if len(os.Args) != 2 || os.Args[1] != "purge-git-worker-v3" {
		return nil, sessionLeaseError("the raw ordinary Git reader has another closed command")
	}
	image := &ordinaryPurgeGitImageV3{}
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	image.code, image.parent = os.NewFile(3, "actual-inherited-fixed-git-code"), os.NewFile(4, "actual-fixed-git-parent-pidfd")
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, image.Close())
		}
	}()
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	image.kernel = os.NewFile(uintptr(fd), "actual-kernel-fixed-git-image")
	image.mapped, err = image.kernel.Stat()
	if err != nil {
		return nil, err
	}
	if err := errors.Join(image.Check(context.Background()), checkPurgeKernelExecEligibilityV2(context.Background(), image.code), CheckCaller()); err != nil {
		return nil, err
	}
	complete = true
	return image, nil
}

func (image *ordinaryPurgeGitImageV3) Check(ctx context.Context) error {
	if image == nil || ctx == nil || image.kernel == nil || image.code == nil || image.parent == nil || image.mapped == nil {
		return sessionLeaseError("the fixed ordinary Git reader lost its raw native image")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(image.code.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.Join(sessionLeaseError("the fixed ordinary Git code is not its actual readable-only inherited role"), err)
	}
	info, err := image.code.Stat()
	_, _, uid, links, known := footprintIdentity(info)
	if err != nil || !known || links != 1 || uid != uint64(os.Getuid()) || !info.Mode().IsRegular() || info.Mode().Perm() != 0o100 || !os.SameFile(info, image.mapped) || info.Size() != image.mapped.Size() || !info.ModTime().Equal(image.mapped.ModTime()) {
		return errors.Join(sessionLeaseError("the raw fixed Git code differs from this actual kernel image"), err)
	}
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	fresh := os.NewFile(uintptr(fd), "current-kernel-fixed-git-image")
	current, statErr := fresh.Stat()
	closeErr := fresh.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(current, image.mapped) {
		return errors.Join(sessionLeaseError("the original fixed Git reader changed its actual mapped image"), statErr, closeErr)
	}
	return unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0)
}

func (image *ordinaryPurgeGitImageV3) Close() error {
	var failure error
	for _, file := range []*os.File{image.kernel, image.code, image.parent} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

func writeOrdinaryGitWorkerHelloV3(ctx context.Context, line *StartupLineV1, hello proto.Hello, image *ordinaryPurgeGitImageV3) (failure error) {
	if image == nil {
		return line.WriteNativeHelloV1(hello)
	}
	if err := image.Check(ctx); err != nil {
		return err
	}
	fd, err := unix.FcntlInt(image.code.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	identity := &nativeHelperIdentityV1{executable: os.NewFile(uintptr(fd), "actual-current-fixed-git-public-code")}
	defer func() { failure = errors.Join(failure, closeOrdinaryPurgePublicIdentityV3(identity)) }()
	identity.user, err = os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	identity.cgroup, err = os.Open("/proc/self/ns/cgroup")
	if err != nil {
		return err
	}
	if err := errors.Join(identity.Check(image.mapped), image.Check(ctx)); err != nil {
		return err
	}
	return sendOrdinaryPurgePublicHelloV3(ctx, line, hello, identity)
}

func acceptOrdinaryGitParentHelloV3(ctx context.Context, line *StartupLineV1, frame proto.Frame, image *ordinaryPurgeGitImageV3) (failure error) {
	if image == nil {
		return acceptOrdinaryGitHelloV2(line, frame, ordinaryGitParentRoleV2)
	}
	if err := errors.Join(image.Check(ctx), checkOrdinaryGitHelloRoleV3(frame, ordinaryGitParentRoleV2)); err != nil {
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
	if err := identity.Check(image.mapped); err != nil {
		return err
	}
	identity.expected = image.mapped
	peer, err := pinOrdinaryPurgePublicPeerV3(line, identity, sender)
	if err != nil {
		return err
	}
	if err := errors.Join(checkOrdinaryPurgeOutsidePeerV3(ctx, peer), image.checkParentV3(ctx, peer, sender)); err != nil {
		return errors.Join(err, peer.Close())
	}
	if err := installOrdinaryPurgePublicPeerV3(line, peer); err != nil {
		return errors.Join(err, peer.Close())
	}
	complete = true
	return nil
}

func (image *ordinaryPurgeGitImageV3) checkParentV3(ctx context.Context, peer *nativeStartupPeerV1, sender startupCredentials) error {
	if err := errors.Join(image.Check(ctx), peer.checkNativeSender(sender)); err != nil {
		return err
	}
	raw, err := ordinaryFDInfoV1(image.parent)
	if err != nil {
		return err
	}
	pid, seen := 0, false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "Pid:" {
			continue
		}
		if seen {
			return sessionLeaseError("the fixed Git parent handle has duplicate native PID observations")
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return sessionLeaseError("the fixed Git parent handle has no live native PID")
		}
		seen = true
	}
	if !seen || pid != peer.pid || pid != sender.pid || pid != os.Getppid() {
		return sessionLeaseError("the fixed Git request came from another original fork parent")
	}
	return errors.Join(peer.checkNativeSender(sender), image.Check(ctx))
}

func (owner *joinedOrdinaryGitInspectionV2) acceptOriginalOrdinaryGitHelloV3(ctx context.Context, line *StartupLineV1, frame proto.Frame) (failure error) {
	if owner.purgeImage == nil {
		return acceptOrdinaryGitHelloV2(line, frame, ordinaryGitWorkerRoleV2)
	}
	if owner.start == nil || owner.start.owner != owner || owner.start.image != owner.purgeImage || owner.creation == nil || owner.creationFD < 0 || int(owner.creation.Fd()) != owner.creationFD || owner.peer == nil {
		return sessionLeaseError("the copied fixed Git hello has no actual original native creator")
	}
	if err := errors.Join(owner.start.checkFrozenV2(ctx), owner.purgeImage.Check(ctx), checkOrdinaryGitHelloRoleV3(frame, ordinaryGitWorkerRoleV2)); err != nil {
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
	if err := identity.Check(owner.start.codeInfo); err != nil {
		return err
	}
	identity.expected = owner.start.codeInfo
	peer, err := pinOrdinaryPurgePublicPeerV3(line, identity, sender)
	if err != nil {
		return err
	}
	if peer.pid != owner.command.Process.Pid || peer.pid != owner.peer.pid || peer.uid != owner.peer.uid {
		return errors.Join(sessionLeaseError("the copied Git socket belongs to another original creation"), peer.Close())
	}
	if err := errors.Join(checkOrdinaryPurgeOutsidePeerV3(ctx, peer), owner.peer.checkNativeSender(sender), checkCreatedHelperArgumentsV1(peer, owner.command.Args), owner.start.checkFrozenV2(ctx)); err != nil {
		return errors.Join(err, peer.Close())
	}
	if err := installOrdinaryPurgePublicPeerV3(line, peer); err != nil {
		return errors.Join(err, peer.Close())
	}
	complete = true
	return nil
}

func (start *ordinaryGitStartV2) checkCodeRolesV3(ctx context.Context) error {
	owner := start.owner
	command := start.command
	if start.image == nil {
		if !slices.Equal(start.args, []string{"/proc/self/fd/3", "git-op", gitOpPurgeInspectV1}) || len(command.ExtraFiles) != 1 || command.ExtraFiles[0] != start.code || start.parent != nil || owner.parent != nil || owner.purgeImage != nil {
			return sessionLeaseError("the ordinary fixed Git start changed its original descriptor roles")
		}
		return nil
	}
	if owner.purgeImage != start.image || start.parent == nil || start.parent != owner.parent || !slices.Equal(start.args, []string{"/proc/self/fd/3", "purge-git-worker-v3"}) || len(command.ExtraFiles) != 2 || command.ExtraFiles[0] != start.code || command.ExtraFiles[1] != start.parent {
		return sessionLeaseError("the copied fixed Git start changed its actual code or parent roles")
	}
	return errors.Join(start.image.Check(ctx), unix.PidfdSendSignal(int(start.parent.Fd()), 0, nil, 0))
}

func checkOrdinaryGitHelloRoleV3(frame proto.Frame, role string) error {
	var hello proto.Hello
	if frame.Type != proto.THello || !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != role {
		return sessionLeaseError("the copied fixed Git helper has another exact native purpose")
	}
	return nil
}
