package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// stopPendingForResume never converts raw socket absence into permission for
// generic kill. Its caller holds op.lock. Every possible pending owner must
// acknowledge cancellation through its own pre-launch gate or remain busy.
func stopPendingForResume(id string, m *Manifest) error {
	raw, err := HolderRequest(id, cancelPendingStartOp, nil, 2*time.Second)
	if err != nil {
		if noHolderSocket(err) && !holderStillRuns(id, m) && pendingUnitStopped(id, m) {
			// No observed holder remains. Recovery does not send a generic
			// kill even if another endpoint appears after this observation.
			return nil
		}
		var perr *proto.Error
		if errors.As(err, &perr) && (perr.Code == "unknown_op" || perr.Code == "starting") {
			name := m.Name
			if settled, ok := settledRenameName(id); ok {
				name = settled
			}
			return UserError("agent_running", "the agent of "+name+" still runs",
				"Attach to it. To start it again, quit the agent first, then resume the session.")
		}
		return pendingResumeBusy(id, err)
	}
	if !validPendingCancellation(raw) {
		return pendingResumeBusy(id, errors.New("the pending cancellation reply did not confirm its stop"))
	}
	deadline := time.Now().Add(stopWait)
	for {
		if !holderStillRuns(id, m) && pendingUnitStopped(id, m) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return pendingResumeBusy(id, errors.New("the acknowledged pending holder is still observed"))
		}
		holderStopWaiting(id)
		time.Sleep(50 * time.Millisecond)
	}
}

func pendingResumeBusy(id string, err error) error {
	return UserError("busy", fmt.Sprintf("the pending holder of session %s could not confirm its stop: %v", id, err),
		"Try again in a moment, or check the host with the host doctor.")
}

// pendingUnitStopped observes only the exact recorded session unit. Unknown
// manager state remains busy. It supplies no service or child stop authority.
func pendingUnitStopped(id string, m *Manifest) bool {
	if m == nil || m.Run.Unit == "" {
		return true
	}
	if m.Run.Unit != UnitName(id) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := systemctl(ctx, "show", m.Run.Unit, "-p", "ActiveState", "--value")
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return state == "inactive" || state == "failed"
}

// stopResumeHolder preserves ordinary stopped-session cleanup. A Starting
// snapshot has its separate positive-cancellation path and never generic kill.
func stopResumeHolder(id string, m *Manifest, pending bool) error {
	if pending {
		if holderStillRuns(id, m) || !pendingUnitStopped(id, m) {
			return errors.New("the pending holder became observable again")
		}
		return nil
	}
	return stopHolder(id, m)
}

// validPendingCancellation accepts only the canonical Result emitted by this
// new same-binary operation. It cannot accept duplicate, future or case-alias
// fields and does not introduce a second general JSON decoder.
func validPendingCancellation(raw []byte) bool {
	return bytes.Equal(raw, []byte(`{"killed":true}`))
}
