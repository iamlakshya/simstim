//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
)

// This producer retains a CURRENT original socket creator for warm none
// operations. It is not a whole-unit stop, a vacant runtime, or a constructor
// from a saved process number. The server independently checks its same actual
// none runtime creation and Start connection before accepting the incoming OP.
func captureNativeNoneWarmV1(ctx context.Context, connection net.Conn, primary string, executable os.FileInfo) (_ *nativeStopInvocationV1, returned error) {
	if ctx == nil || connection == nil || !validID(primary) {
		return nil, sessionLeaseError("the unwalled warm source has no original physical connection")
	}
	peer, err := pinUnverifiedNativeHelperPeerV1(connection)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, peer.Close())
		}
	}()
	if peer.uid != uint32(os.Getuid()) {
		return nil, sessionLeaseError("the unwalled supervisor belongs to another native account")
	}
	data, err := peer.proc.ReadFile("cmdline")
	if err != nil {
		return nil, err
	}
	argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(argv) != 3 || argv[2] != primary || argv[1] != "supervise" && argv[1] != "supervise-restore-v1" {
		return nil, sessionLeaseError("the current unwalled socket creator has another closed helper command")
	}
	result := &nativeStopInvocationV1{peer: peer, primary: primary, executable: executable, argv: argv, unmanagedNoneWarm: true}
	if err := result.checkNoneWarmV1(ctx, false); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}

func (invocation *nativeStopInvocationV1) checkNoneWarmV1(ctx context.Context, identity bool) error {
	if invocation == nil || !invocation.unmanagedNoneWarm || ctx == nil || invocation.peer == nil || invocation.manager != nil || invocation.cgroup != nil || !validID(invocation.primary) {
		return sessionLeaseError("the original unwalled warm invocation is absent")
	}
	if err := errors.Join(ctx.Err(), invocation.peer.checkLive()); err != nil {
		return err
	}
	if identity {
		return errors.Join(checkNativeHelperExecutableV1(invocation.peer, invocation.executable, invocation.argv), invocation.peer.checkLive(), ctx.Err())
	}
	return errors.Join(checkCreatedHelperArgumentsV1(invocation.peer, invocation.argv), invocation.peer.checkLive(), ctx.Err())
}
