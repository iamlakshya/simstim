//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// This is the adopted CLOSED operator boundary, not a generic peer override.
// It observes the actual socket process, not namespace descriptors or code
// tuples supplied by that peer. It grants no OP or resource authority.
type purgeRequestPeerV3 struct {
	native *nativeStartupPeerV1
	image  *purgeAttemptImageV2
}

// The retained original code object is a PUBLIC observation only. The fixed
// command supplies DATA; current native credentials, pinned lifetime and
// outside placement enforce the Root-adopted operator boundary. This method
// does not authenticate a parent's current mm or issue a session source.
func acceptPurgeOperatorHelloV3(ctx context.Context, line *StartupLineV1, image *purgeAttemptImageV2, originalParent bool) (result *purgeRequestPeerV3, returned error) {
	if ctx == nil || line == nil || image == nil {
		return nil, placementError("the purge DATA connector has no current image and original line")
	}
	if err := image.Check(ctx); err != nil {
		return nil, err
	}
	frame, err := line.ReadFrame()
	if err != nil {
		return nil, err
	}
	identity, sender, err := line.takeNativeHelperIdentityFrameV1(frame, proto.THello)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, identity.Close())
		}
	}()
	var hello proto.Hello
	if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Role != "purge-data-parent-v3" && hello.Role != "purge-data-relay-v3" || originalParent && hello.Role != "purge-data-parent-v3" {
		return nil, placementError("the actual purge DATA hello has another closed purpose")
	}
	image.mu.Lock()
	expected := image.originalInfo
	image.mu.Unlock()
	if hello.Role == "purge-data-relay-v3" {
		if err := image.checkRelayPublicCodeV3(ctx, identity.executable); err != nil {
			return nil, err
		}
		expected, err = identity.executable.Stat()
		if err != nil {
			return nil, err
		}
	}
	if err := identity.Check(expected); err != nil {
		return nil, err
	}
	peer, err := pinUnverifiedNativeHelperPeerV1(line.conn)
	if err != nil {
		return nil, err
	}
	peer.identity, identity.expected = identity, expected
	owner := &purgeRequestPeerV3{native: peer, image: image}
	if err := owner.CheckSender(ctx, sender); err != nil {
		return nil, errors.Join(err, peer.Close())
	}
	if originalParent {
		if err := image.checkOriginalOperatorParentV3(ctx, owner, sender); err != nil {
			return nil, errors.Join(err, peer.Close())
		}
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.nativePeer != nil {
		return nil, errors.Join(placementError("the purge DATA line ended or changed its original connector"), peer.Close())
	}
	line.nativePeer = peer
	complete = true
	return owner, nil
}

func (owner *purgeRequestPeerV3) Check(ctx context.Context) error {
	if owner == nil || owner.native == nil || ctx == nil {
		return placementError("the actual purge connector is absent")
	}
	peer := owner.native
	if peer.uid != uint32(os.Getuid()) || peer.pid <= 0 {
		return errCallerInside
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), peer.checkLive()); err != nil {
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
	if err != nil || managerNS != "" && (selfNS != managerNS || !below) {
		return errors.Join(errCallerNamespace, err)
	}
	if peer.identity == nil {
		return placementError("the actual purge connector has no native namespace observations")
	}
	if err := peer.identity.Check(peer.identity.expected); err != nil {
		return err
	}
	// Kernel SO_PEERCRED is the operator identity. Observe every current UID
	// too, so a changed-credential peer is not treated as that operator.
	status, err := peer.proc.ReadFile("status")
	if err != nil {
		return errors.Join(errCallerInside, err)
	}
	seen := false
	for _, line := range strings.Split(string(status), "\n") {
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

func (owner *purgeRequestPeerV3) CheckSender(ctx context.Context, sender startupCredentials) error {
	if owner == nil || owner.native == nil || sender.pid != owner.native.pid || sender.uid != owner.native.uid {
		return placementError("the current purge frame differs from its original socket creator")
	}
	if owner.image == nil || owner.native.identity == nil {
		return placementError("the purge DATA frame has no same actual protected receiver")
	}
	if err := errors.Join(owner.Check(ctx), owner.image.checkRelayPublicCodeV3(ctx, owner.native.identity.executable)); err != nil {
		return err
	}
	return owner.native.checkNativeSender(sender)
}

func (owner *purgeRequestPeerV3) Close() error {
	if owner == nil || owner.native == nil {
		return nil
	}
	return owner.native.Close()
}

// This is the Root-adopted DATA requester boundary. It proves the actual live
// outside fork parent and current operator, not that parent's current mm code.
// The child independently creates its own domain and every original OP.
func (image *purgeAttemptImageV2) checkOriginalOperatorParentV3(ctx context.Context, owner *purgeRequestPeerV3, sender startupCredentials) error {
	if image == nil || owner == nil || owner.native == nil {
		return placementError("the actual purge DATA requester is absent")
	}
	if err := errors.Join(image.Check(ctx), owner.CheckSender(ctx, sender)); err != nil {
		return err
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.closed || image.parent == nil {
		return placementError("the original native parent handle ended")
	}
	if err := unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0); err != nil {
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
			return placementError("the original requester handle has duplicate native PID observations")
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			return placementError("the original requester handle has no current native PID")
		}
		seen = true
	}
	if !seen || owner.native.pid != pid || sender.pid != pid || os.Getppid() != pid {
		return placementError("the purge DATA request did not come from its original native fork parent")
	}
	return errors.Join(owner.CheckSender(ctx, sender), unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0))
}
