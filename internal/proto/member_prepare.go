package proto

import (
	"encoding/json"
	"math"
	"strings"
)

// GuestMemberPrepareV1 guards ordered preparation, Store and final consumption.
const GuestMemberPrepareV1 = "member-prepare-v1"

// CtlMemberPrepare prepares infrastructure without starting a holder.
const CtlMemberPrepare = "member.prepare-v1"

// CtlMemberStore completes only the reserved member's required Store work.
const CtlMemberStore = "member.store-v1"

// CtlMemberAbort releases only an exact unconsumed prepared reservation.
const CtlMemberAbort = "member.abort-v1"

// CtlMemberAbortResult reports limited cleanup state, never startup authority.
// State is absent, aborted, failed, consumed or busy; it proves no general join.
type CtlMemberAbortResult struct {
	Version int    `json:"version"`
	State   string `json:"state"`
}

// CtlMemberPrepareArgs uses the same complete start intent for both sub-ops.
// Target binds the claim to either Start or Spawn; neither may substitute it.
type CtlMemberPrepareArgs struct {
	Version int          `json:"version"`
	Target  string       `json:"target"`
	Start   CtlStartArgs `json:"start"`
}

// CtlMemberPrepareResult returns a claim owned by this guest init and run.
type CtlMemberPrepareResult struct {
	Version int    `json:"version"`
	Claim   string `json:"claim"`
}

// CheckMemberPrepareFrames checks every eventual control frame before admission.
// A claim is exactly 32 random bytes encoded as 64 lower-case hex characters.
func CheckMemberPrepareFrames(target string, args CtlStartArgs) error {
	if target != CtlStart && target != CtlSpawn {
		return &Error{Code: "bad_request", Message: "the prepared member has no valid launch operation", Hint: "Prepare the member for Start or Spawn with matching helpers."}
	}
	args.PreparedClaim = strings.Repeat("0", 64)
	operations := []struct {
		op   string
		args any
	}{
		{target, args},
		{CtlMemberStore, CtlMemberPrepareArgs{Version: 1, Target: target, Start: args}},
		{CtlMemberAbort, CtlMemberPrepareArgs{Version: 1, Target: target, Start: args}},
	}
	args.PreparedClaim = ""
	operations = append(operations, struct {
		op   string
		args any
	}{CtlMemberPrepare, CtlMemberPrepareArgs{Version: 1, Target: target, Start: args}})
	for _, operation := range operations {
		raw, err := json.Marshal(operation.args)
		if err != nil {
			return err
		}
		frame, err := json.Marshal(Request{ID: math.MaxUint64, Op: operation.op, Args: raw})
		if err != nil {
			return err
		}
		if len(frame)+1 > 64<<10 {
			return &Error{Code: "bad_request", Message: "the prepared member intent exceeds its control frame bound", Hint: "Reduce the member's control metadata and start it again."}
		}
	}
	return nil
}
