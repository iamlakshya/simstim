//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// Absence keeps the ordinary same-image path. A registered producer which
// fails its checks cannot fall back to a public code tuple or another image.
func ordinaryPurgeImageForPlacementV3(ctx context.Context, p *placement) (*purgeAttemptImageV2, error) {
	if ctx == nil || p == nil {
		return nil, sessionLeaseError("the ordinary purge image has no original placement or lifetime")
	}
	purgeAttemptDomainsV3.Lock()
	owner := purgeAttemptDomainsV3.owners[p]
	purgeAttemptDomainsV3.Unlock()
	if owner == nil {
		return nil, ctx.Err()
	}
	return purgeAttemptImageForPlacementV3(ctx, p)
}

func duplicateOrdinaryPurgeCodeV3(ctx context.Context, image *purgeAttemptImageV2) (*os.File, error) {
	if image == nil {
		return nil, sessionLeaseError("the ordinary purge has no actual current copied image")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if err := image.checkLockedV2(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(image.copied.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "actual-ordinary-purge-current-code"), nil
}

func (owner *ordinaryHolderCaptureV2) writeOrdinaryCaptureHelloV3(ctx context.Context, hello proto.Hello) error {
	if owner.purgeImage == nil {
		return owner.line.WriteNativeHelloV1(hello)
	}
	return writeOrdinaryPurgeImageHelloV3(ctx, owner.line, hello, owner.purgeImage)
}

// Only this capture's independently installed current image chooses the old
// holder's ORIGINAL code. The placement continues to name the actual COPY.
func (owner *ordinaryHolderCaptureV2) acceptOrdinaryCaptureHelloV3(ctx context.Context, frame proto.Frame) (failure error) {
	if owner.purgeImage == nil {
		return owner.line.AcceptNativeHelloV1(frame)
	}
	if owner.operation == nil || owner.operation.native == nil || owner.operation.native.borrowed || !owner.operation.Valid(owner.operation.Primary()) {
		return sessionLeaseError("the copied ordinary capture lost its same original operation")
	}
	if err := errors.Join(owner.purgeImage.Check(ctx), owner.origin.checkIdentity(), owner.checkOriginalSocketV1()); err != nil {
		return err
	}
	identity, sender, err := owner.line.takeNativeHelperIdentityFrameV1(frame, proto.THello)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, identity.Close())
		}
	}()
	owner.purgeImage.mu.Lock()
	expected := owner.purgeImage.originalInfo
	owner.purgeImage.mu.Unlock()
	if err := identity.Check(expected); err != nil {
		return err
	}
	identity.expected = expected
	peer, err := pinOrdinaryPurgePublicPeerV3(owner.line, identity, sender)
	if err != nil {
		return err
	}
	if err := installOrdinaryPurgePublicPeerV3(owner.line, peer); err != nil {
		return errors.Join(err, peer.Close())
	}
	complete = true
	return nil
}

func (h *Holder) acceptOrdinaryControlHelloV3(line *StartupLineV1, reversible bool) (failure error) {
	if !reversible {
		return line.AcceptNativeHelloV1(proto.Frame{Type: proto.THello})
	}
	// This existing holder, root, main and locked OFD remain the resource
	// producer. Exact public bytes only restrict the outside DATA requester.
	if h.ordinary == nil || h.ordinary.native == nil || h.main == nil || h.main.pidfd < 0 {
		return sessionLeaseError("the ordinary purge receiver lost its original holder creation")
	}
	ctx := context.Background()
	if err := h.ordinary.native.check(ctx); err != nil {
		return err
	}
	original, err := currentNativeHelperIdentityV1()
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, original.Close()) }()
	expected, err := original.executable.Stat()
	if err != nil {
		return err
	}
	identity, sender, err := line.takeNativeHelperIdentityFrameV1(proto.Frame{Type: proto.THello}, proto.THello)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, identity.Close())
		}
	}()
	if err := checkPurgePublicCodeBytesV3(ctx, original.executable, identity.executable, expected); err != nil {
		return err
	}
	actual, err := identity.executable.Stat()
	if err != nil {
		return err
	}
	if err := identity.Check(actual); err != nil {
		return err
	}
	identity.expected = actual
	peer, err := pinOrdinaryPurgePublicPeerV3(line, identity, sender)
	if err != nil {
		return err
	}
	if err := checkOrdinaryPurgeOutsidePeerV3(ctx, peer); err != nil {
		return errors.Join(err, peer.Close())
	}
	if err := h.ordinary.native.check(ctx); err != nil {
		return errors.Join(err, peer.Close())
	}
	if err := installOrdinaryPurgePublicPeerV3(line, peer); err != nil {
		return errors.Join(err, peer.Close())
	}
	complete = true
	return nil
}

// Both callers validate their own actual current-code producer before this
// public serializer. It cannot install an expected image or lend an OP.
func sendOrdinaryPurgePublicHelloV3(ctx context.Context, line *StartupLineV1, hello proto.Hello, identity *nativeHelperIdentityV1) error {
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("the ordinary purge hello has no native Unix connection")
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return proto.SendUnixFrameV1(connection, proto.THello, raw, []*os.File{identity.executable, identity.user, identity.cgroup})
}

func writeOrdinaryPurgeImageHelloV3(ctx context.Context, line *StartupLineV1, hello proto.Hello, image *purgeAttemptImageV2) (failure error) {
	code, err := duplicateOrdinaryPurgeCodeV3(ctx, image)
	if err != nil {
		return err
	}
	identity := &nativeHelperIdentityV1{executable: code}
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

func closeOrdinaryPurgePublicIdentityV3(identity *nativeHelperIdentityV1) error {
	var failure error
	for _, file := range []*os.File{identity.executable, identity.user, identity.cgroup} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

func pinOrdinaryPurgePublicPeerV3(line *StartupLineV1, identity *nativeHelperIdentityV1, sender startupCredentials) (*nativeStartupPeerV1, error) {
	peer, err := pinUnverifiedNativeHelperPeerV1(line.conn)
	if err != nil {
		return nil, err
	}
	if err := peer.checkNativeSender(sender); err != nil {
		return nil, errors.Join(err, peer.Close())
	}
	peer.identity = identity
	return peer, nil
}

func installOrdinaryPurgePublicPeerV3(line *StartupLineV1, peer *nativeStartupPeerV1) error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.nativePeer != nil {
		return sessionLeaseError("the ordinary purge line ended or already has another native peer")
	}
	line.nativePeer = peer
	return nil
}

// The receiver may itself be a holder in its session unit. Only its pinned
// outside requester must satisfy this closed operator policy. Namespace
// handles remain public observations; no peer-current-mm claim is made.
func checkOrdinaryPurgeOutsidePeerV3(ctx context.Context, peer *nativeStartupPeerV1) error {
	if ctx == nil || peer == nil || peer.identity == nil || peer.uid != uint32(os.Getuid()) {
		return errCallerInside
	}
	if err := errors.Join(ctx.Err(), peer.Check()); err != nil {
		return err
	}
	raw, err := peer.proc.ReadFile("cgroup")
	if err != nil {
		return errors.Join(errCallerInside, err)
	}
	paths, below := cgroupPaths(raw)
	if len(paths) == 0 || !below || placeAmong(paths, "") != placeOutside {
		return errCallerInside
	}
	selfNS, managerNS, err := CgroupNamespaces()
	if err != nil || managerNS != "" && selfNS != managerNS {
		return errors.Join(errCallerNamespace, err)
	}
	raw, err = peer.proc.ReadFile("status")
	if err != nil {
		return errors.Join(errCallerInside, err)
	}
	seen := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" {
			continue
		}
		if seen || len(fields) != 5 {
			return errCallerInside
		}
		seen = true
		for _, value := range fields[1:] {
			uid, err := strconv.ParseUint(value, 10, 32)
			if err != nil || uid != uint64(os.Getuid()) {
				return errCallerInside
			}
		}
	}
	if !seen {
		return errCallerInside
	}
	return peer.checkLive()
}
