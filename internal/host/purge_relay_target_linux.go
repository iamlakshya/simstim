//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// Target observations constrain a DATA connection only. They do not adopt a
// code factory, root-cleanup source, session operation or expired vector.
type purgeRelayTargetV3 struct {
	image                *purgeRelayImageV3
	home, root           *os.Root
	homeRef, rootRef     purgeRef
	rootInfo, socketInfo os.FileInfo
	socket, directory    *os.File
	leaf                 string
	line                 *StartupLineV1
	peer                 *purgeRequestPeerV3
	original             *purgeAttemptExecV1
}

func openPurgeRelayTargetV3(ctx context.Context, image *purgeRelayImageV3, args api.PurgeArgs) (result *purgeRelayTargetV3, returned error) {
	return openPurgeRelayTargetForOriginalV3(ctx, image, args, nil)
}

func openPurgeRelayTargetForOriginalV3(ctx context.Context, image *purgeRelayImageV3, args api.PurgeArgs, original *purgeAttemptExecV1) (result *purgeRelayTargetV3, returned error) {
	if image == nil || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
		return nil, placementError("the raw relay has no original fixed removal data")
	}
	if err := image.Check(ctx); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if original != nil {
		if original.args != args {
			return nil, placementError("the retained original invocation names another relay request")
		}
		if err := original.checkRetainedCreationV3(ctx); err != nil {
			return nil, err
		}
	}
	owner := &purgeRelayTargetV3{image: image, original: original}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	owner.home, err = cachefs.OpenSafeAncestor(home)
	if err != nil {
		return nil, err
	}
	owner.homeRef, err = nativeWorktreeDirectoryRefV1(owner.home)
	if err != nil || !purgeSafeParentRefV1(owner.homeRef) {
		return nil, errors.Join(placementError("the raw relay's actual home target is unknown"), err)
	}
	leaf := "." + brand.Name + "-attempt-" + args.Nonce
	for _, candidate := range []string{leaf, leaf + ".closing"} {
		owner.root, err = cachefs.OpenPrivateChild(owner.home, candidate, false)
		if err == nil {
			owner.leaf = candidate
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if owner.root == nil {
		return nil, UserError("purge_custody", "the original removal vector has no live private endpoint", "Keep the host saved and inspect the original removal. A fresh helper cannot recreate expired native custody.")
	}
	owner.rootRef, err = nativeWorktreeDirectoryRefV1(owner.root)
	if err != nil {
		return nil, err
	}
	owner.directory, err = owner.root.Open(".")
	if err != nil {
		return nil, err
	}
	owner.rootInfo, err = owner.directory.Stat()
	if err != nil {
		return nil, err
	}
	owner.socket, err = owner.root.OpenFile("attempt.sock", unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	owner.socketInfo, err = owner.socket.Stat()
	if err != nil || owner.socketInfo.Mode() != os.ModeSocket|0o600 {
		return nil, errors.Join(placementError("the original relay target has no exact private native socket"), err)
	}
	if err := owner.Check(ctx); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", fmt.Sprintf("/proc/self/fd/%d/attempt.sock", owner.directory.Fd()))
	if err != nil {
		return nil, err
	}
	owner.line, err = NewNativeStartupLineV1(connection)
	if err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = connection.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if err := owner.acceptHelloV3(ctx); err != nil {
		return nil, err
	}
	if err := image.writeHelloV3(ctx, owner.line); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *purgeRelayTargetV3) Check(ctx context.Context) error {
	if owner == nil || owner.image == nil || owner.home == nil || owner.root == nil || owner.directory == nil || owner.socket == nil {
		return placementError("the raw relay lost its original native target observations")
	}
	if err := errors.Join(owner.image.Check(ctx), owner.homeRef.matchesRoot(owner.home), owner.rootRef.matchesRoot(owner.root)); err != nil {
		return err
	}
	if owner.original != nil {
		if err := owner.original.checkRetainedCreationV3(ctx); err != nil {
			return err
		}
	}
	actual, err := owner.directory.Stat()
	_, _, _, links, known := footprintIdentity(actual)
	if err != nil || !known || !os.SameFile(actual, owner.rootInfo) {
		return errors.Join(placementError("the original relay target directory changed"), err)
	}
	socket, err := owner.socket.Stat()
	_, _, _, socketLinks, socketKnown := footprintIdentity(socket)
	if err != nil || !socketKnown || !os.SameFile(socket, owner.socketInfo) {
		return errors.Join(placementError("the original relay target socket changed"), err)
	}
	if links == 0 && socketLinks == 0 {
		return nil
	} // SAME retained observations after own terminal unlink; never a Source.
	current, err := owner.home.Lstat(owner.leaf)
	if err != nil || !os.SameFile(current, owner.rootInfo) {
		return errors.Join(placementError("the relay target lost its original private parent binding"), err)
	}
	current, err = owner.root.Lstat("attempt.sock")
	if err != nil || !os.SameFile(current, owner.socketInfo) {
		return errors.Join(placementError("the relay target socket lost its original native name"), err)
	}
	return nil
}

func (owner *purgeRelayTargetV3) acceptHelloV3(ctx context.Context) (returned error) {
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	identity, sender, err := owner.line.takeNativeHelperIdentityFrameV1(frame, proto.THello)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, identity.Close())
		}
	}()
	var hello proto.Hello
	if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Role != nativePurgeAttemptRoleV2 {
		return placementError("the actual relay endpoint has another native closed purpose")
	}
	if err := checkPurgePublicCodeBytesV3(ctx, owner.image.readable, identity.executable, owner.image.mapped); err != nil {
		return err
	}
	expected, err := identity.executable.Stat()
	if err != nil {
		return err
	}
	if err := identity.Check(expected); err != nil {
		return err
	}
	native, err := pinUnverifiedNativeHelperPeerV1(owner.line.conn)
	if err != nil {
		return err
	}
	native.identity, identity.expected = identity, expected
	if owner.original != nil && (owner.original.process == nil || native.pid != owner.original.process.Pid || native.uid != owner.original.peer.uid) {
		return errors.Join(placementError("the relay target differs from its retained actual original creation"), native.Close())
	}
	peer := &purgeRequestPeerV3{native: native}
	if err := errors.Join(native.checkNativeSender(sender), peer.Check(ctx), owner.Check(ctx)); err != nil {
		return errors.Join(err, native.Close())
	}
	if err := checkCreatedHelperArgumentsV1(native, []string{"/proc/self/fd/4", "purge-attempt-v2"}); err != nil {
		return errors.Join(err, native.Close())
	}
	owner.line.mu.Lock()
	owner.line.nativePeer = native
	owner.line.mu.Unlock()
	owner.peer = peer
	complete = true
	return nil
}

func (owner *purgeRelayTargetV3) checkSenderV3(ctx context.Context, sender startupCredentials) error {
	if owner == nil || owner.peer == nil || owner.peer.native == nil || owner.peer.native.identity == nil {
		return placementError("the relay response has no original live native target")
	}
	if err := errors.Join(owner.Check(ctx), owner.peer.Check(ctx), owner.peer.native.checkNativeSender(sender)); err != nil {
		return err
	}
	if err := checkPurgePublicCodeBytesV3(ctx, owner.image.readable, owner.peer.native.identity.executable, owner.image.mapped); err != nil {
		return err
	}
	return owner.peer.native.checkLive()
}

func (owner *purgeRelayTargetV3) Close() error {
	if owner == nil {
		return nil
	}
	var failure error
	if owner.line != nil {
		failure = errors.Join(failure, owner.line.Close())
	}
	for _, file := range []*os.File{owner.socket, owner.directory} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	for _, root := range []*os.Root{owner.root, owner.home} {
		if root != nil {
			failure = errors.Join(failure, root.Close())
		}
	}
	return failure
}
