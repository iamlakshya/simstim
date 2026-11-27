package jail

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// GvisorControllerAggregateV1 is issued only by this actual command after
// current original C code/task/namespace/socket witnesses, its final FS frame
// and the signed fixed controller's exact local aggregate. A field or copied
// value cannot issue it. The original host operation must still gate lending
// the SAME withheld guest descriptor; it is never part of inherited setup.
type GvisorControllerAggregateV1 struct {
	original *GvisorControllerAggregateV1
	owner    *gvisorControllerOwnerV1
	final    *gvisorControllerFinalV1
	loan     *gvisorControllerGuestLoanV1
}
type gvisorControllerGuestLoanV1 struct {
	original *GvisorControllerAggregateV1
	guest    *os.File
}

// AwaitGvisorControllerV1 joins the original native controller witnesses and
// final aggregate. It transfers no guest or private operation itself.
func (c *Cmd) AwaitGvisorControllerV1(ctx context.Context) (*GvisorControllerAggregateV1, error) {
	if c == nil || ctx == nil {
		return nil, gvisorHandoffFailureV2("the original controller request lifetime is absent")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	var owner *gvisorControllerOwnerV1
	if c.gvisorOuter != nil {
		owner = c.gvisorOuter.controller
	}
	c.mu.Unlock()
	if owner == nil {
		return nil, gvisorHandoffFailureV2("the actual command has no original closed controller purpose")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.command != c || owner.original != owner || owner.aggregate != nil {
		return nil, gvisorHandoffFailureV2("the original controller aggregate was copied or reused")
	}
	if err := owner.receiveWitnessV1(); err != nil {
		return nil, err
	}
	witness := owner.witness
	native, err := owner.currentNativeV1()
	if err != nil {
		return nil, err
	}
	peer := gvisorControllerPeerV1(native.pid)
	if err := native.Close(); err != nil {
		return nil, err
	}
	if _, err := receiveGvisorControllerPacketV1(witness.endpoint, []byte{'F', 'S', 'V', 1, 1}, 0, peer, false); err != nil {
		return nil, err
	}
	if err := witness.currentV1(); err != nil {
		return nil, err
	}
	witness.final = &gvisorControllerFinalV1{original: witness}
	// AGV is accepted only from the SAME current protected fixed controller
	// AFTER its native final receipt. Its signed body joins its own exact two
	// original children/final receipts/parked gofer vector before this send.
	if _, err := receiveGvisorControllerPacketV1(witness.endpoint, []byte{'A', 'G', 'V', 1}, 0, peer, false); err != nil {
		return nil, err
	}
	if err := witness.currentV1(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	aggregate := &GvisorControllerAggregateV1{owner: owner, final: witness.final}
	aggregate.original = aggregate
	owner.aggregate = aggregate
	return aggregate, nil
}

func checkGvisorControllerEmptyV1(file *os.File) error {
	data := make([]byte, 1)
	ancillary := make([]byte, unix.CmsgSpace(10*4)+unix.CmsgSpace(unix.SizeofUcred))
	n, oobn, _, _, err := unix.Recvmsg(int(file.Fd()), data, ancillary, unix.MSG_PEEK|unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	if errors.Is(err, unix.EAGAIN) && n <= 0 && oobn == 0 {
		return nil
	}
	messages, parseErr := unix.ParseSocketControlMessage(ancillary[:max(oobn, 0)])
	var closeErr error
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			rights, err := unix.ParseUnixRights(&message)
			closeErr = errors.Join(closeErr, err)
			for _, fd := range rights {
				closeErr = errors.Join(closeErr, unix.Close(fd))
			}
		}
	}
	return errors.Join(err, parseErr, closeErr, gvisorHandoffFailureV2("the original controller has unexpected queued data or closed before private release"))
}

// ReleaseGuestV1 sends only this original constructor's withheld stream. The
// production host calls it inside its SAME operation's current-read guard,
// after rechecking the original boot receipt. It accepts no FD, role, argv,
// success bit, operation DTO or replacement owner.
func (aggregate *GvisorControllerAggregateV1) ReleaseGuestV1(ctx context.Context) error {
	if aggregate == nil || aggregate.owner == nil || ctx == nil {
		return gvisorHandoffFailureV2("private controller release has no original aggregate or operation lifetime")
	}
	owner := aggregate.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if aggregate.original != aggregate || owner.original != owner || owner.aggregate != aggregate || owner.closed || owner.guest == nil || owner.witness == nil || owner.witness.final == nil || aggregate.final != owner.witness.final || aggregate.final.original != owner.witness {
		return gvisorHandoffFailureV2("private controller release was copied, replaced or consumed")
	}
	if aggregate.loan != nil {
		if aggregate.loan.original != aggregate || aggregate.loan.guest == nil {
			return gvisorHandoffFailureV2("the original one-use controller guest loan changed")
		}
		return gvisorHandoffFailureV2("the original controller guest loan was already attempted")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := owner.command.ctx.Err(); err != nil {
		return err
	}
	if err := owner.witness.currentV1(); err != nil {
		return err
	}
	if err := checkGvisorGuestStreamV1(owner.guest); err != nil {
		return err
	}
	if err := checkGvisorControllerEmptyV1(owner.witness.endpoint); err != nil {
		return err
	}
	oob := unix.UnixRights(int(owner.guest.Fd()))
	oob = append(oob, unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid())})...)
	if err := ctx.Err(); err != nil {
		return err
	}
	data := []byte{'C', 'F', 'D', 1}
	// Freeze this exact loan before the syscall. A failed/uncertain send may
	// only clean up through this original owner; it can never retry the right.
	aggregate.loan = &gvisorControllerGuestLoanV1{original: aggregate, guest: owner.guest}
	n, err := unix.SendmsgN(int(owner.witness.endpoint.Fd()), data, oob, nil, unix.MSG_NOSIGNAL)
	if err != nil || n != len(data) {
		return errors.Join(err, gvisorHandoffFailureV2("the one-use original controller guest transfer failed"))
	}
	// The kernel atomically delivered this seqpacket/right. No retry may lend
	// the original capability again even when closing this outside copy fails.
	guest := owner.guest
	owner.guest = nil
	return guest.Close()
}
