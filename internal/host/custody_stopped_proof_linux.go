//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func (stopped *managerStoppedV1) captureRolesV1(ctx context.Context) error {
	owner := stopped.capture
	args, err := json.Marshal(proto.NativeStopArgsV1{Version: 1, Primary: owner.operation.Primary(), Nonce: owner.state.Nonce})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = owner.line.SetDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = owner.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	for index, role := range []string{"custody-root-v1", "custody-holder-v1", "custody-cgroup-v1", "custody-pidfd-v1", "custody-claim-v1"} {
		id := uint64(index + 20)
		if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: id, Op: proto.ManagerCustodyStopRoleV1, Args: args}); err != nil {
			return err
		}
		frame, err := owner.line.ReadFrame()
		if err != nil {
			return err
		}
		var response proto.Response
		var state proto.NativeStopStateV1
		if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK || !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != owner.state {
			return errors.Join(auxiliaryUnavailableV1("the live original main did not lend its exact manager custody role"), owner.line.RejectAncillary())
		}
		file, sender, err := owner.line.takeLoanFile()
		if err != nil {
			return err
		}
		if err := errors.Join(owner.invocation.peer.checkNativeSender(sender), owner.invocation.Check(ctx)); err != nil {
			return errors.Join(err, file.Close())
		}
		stopped.roles[role] = file
	}
	return nil
}

func (stopped *managerStoppedV1) checkLiveRolesV1(ctx context.Context) error {
	owner := stopped.capture
	if len(stopped.roles) != custodyBundleSizeV1 {
		return auxiliaryUnavailableV1("the live main supplied no complete original custody bundle")
	}
	root, err := stopped.roles["custody-root-v1"].Stat()
	if err != nil {
		return err
	}
	actual, err := owner.operation.native.root.Stat(".")
	if err != nil || !os.SameFile(root, actual) {
		return errors.Join(auxiliaryUnavailableV1("the live custody role changed its original session root"), err)
	}
	if _, err := cachefs.CaptureFile(owner.operation.native.root, holderLockFile, stopped.roles["custody-holder-v1"], 0o600); err != nil {
		return err
	}
	if err := checkCustodyFlockV1(stopped.roles["custody-holder-v1"]); err != nil {
		return err
	}
	group, err := stopped.roles["custody-cgroup-v1"].Stat()
	if err != nil {
		return err
	}
	actual, err = owner.invocation.cgroup.Stat()
	if err != nil || !os.SameFile(group, actual) {
		return errors.Join(auxiliaryUnavailableV1("the live custody role changed its original whole cgroup"), err)
	}
	if err := checkCustodyLivePidfdV1(stopped.roles["custody-pidfd-v1"], owner.invocation.peer); err != nil {
		return err
	}
	return (&custodyClaimV1{file: stopped.roles["custody-claim-v1"]}).peek()
}

func checkCustodyLivePidfdV1(file *os.File, peer *nativeStartupPeerV1) error {
	if file == nil || peer == nil {
		return auxiliaryUnavailableV1("the original main pidfd role is absent")
	}
	if err := peer.checkLive(); err != nil {
		return err
	}
	if err := unix.PidfdSendSignal(int(file.Fd()), 0, nil, 0); err != nil {
		return err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil || len(raw) > 4096 {
		return errors.Join(auxiliaryUnavailableV1("the original pidfd role has no bounded kernel process identity"), err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "Pid:" {
			if found || len(fields) != 2 || fields[1] != strconv.Itoa(peer.pid) {
				return auxiliaryUnavailableV1("the custody pidfd belongs to another live native process")
			}
			found = true
		}
	}
	if !found {
		return auxiliaryUnavailableV1("the original custody pidfd has no kernel PID field")
	}
	return peer.checkLive()
}
