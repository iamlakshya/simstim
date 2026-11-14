package host

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// AutomaticRestorePlan is controller-only intent derived under the existing
// primary operation lease. No client/guest row or secret value supplies it.
type AutomaticRestorePlan struct {
	Version       int                 `json:"version"`
	Snapshot      api.SandboxSnapshot `json:"snapshot"`
	PrimaryWanted bool                `json:"primary_wanted"`
	PrimaryStatus Status              `json:"primary_status"`
	MemberIDs     []string            `json:"members"`
}

// AutomaticRestoreSelection retains protected pre-boot status until the same
// snapshot codec pins the staged new controller record before its one Save.
type AutomaticRestoreSelection struct {
	PrimaryWanted bool
	PrimaryStatus Status
	MemberIDs     []string
}

// SelectAutomaticRestore validates every selected saved command before any
// launch or publication. Its caller already owns primary op.lock.
func SelectAutomaticRestore(m *Manifest) (*AutomaticRestoreSelection, error) {
	if m == nil || m.path != "" || m.SandboxOf != "" || m.Wall == nil {
		return nil, nil
	}
	members, err := SandboxMembers(m.ID)
	if err != nil {
		return nil, err
	}
	status := m.Status
	if len(m.AutomaticRestore) > 0 {
		prior, err := ValidateAutomaticRestore(m)
		if err != nil {
			return nil, err
		}
		status = prior.PrimaryStatus
	}
	wanted := !m.Ended() // only the protected controller lifecycle says ended
	selection := &AutomaticRestoreSelection{PrimaryWanted: wanted, PrimaryStatus: status, MemberIDs: []string{}}
	if selection.PrimaryStatus.ExitCode != nil {
		code := *selection.PrimaryStatus.ExitCode
		selection.PrimaryStatus.ExitCode = &code
	}
	for _, member := range members {
		if member.Ended() || member.Restart.Policy != "resume" {
			continue
		}
		if _, err := ResumeSandboxMember(member, m); err != nil {
			return nil, err
		}
		selection.MemberIDs = append(selection.MemberIDs, member.ID)
	}
	slices.Sort(selection.MemberIDs)
	return selection, nil
}

// PinAutomaticRestore uses the one structural snapshot implementation on the
// staged protected record. Its caller saves it once under the SAME lease.
func PinAutomaticRestore(m *Manifest, selection *AutomaticRestoreSelection) error {
	if selection == nil {
		return nil
	}
	members, err := SandboxMembers(m.ID)
	if err != nil {
		return err
	}
	snapshot, err := snapshotFromProtected(m, members, m.ID)
	if err != nil {
		return err
	}
	plan := AutomaticRestorePlan{Version: 1, Snapshot: *snapshot, PrimaryWanted: selection.PrimaryWanted, PrimaryStatus: selection.PrimaryStatus, MemberIDs: slices.Clone(selection.MemberIDs)}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if _, err := DecodeAutomaticRestore(raw); err != nil {
		return err
	}
	m.AutomaticRestore = append(json.RawMessage(nil), raw...)
	return nil
}

// DecodeAutomaticRestore interprets only a strict supported local plan. The
// Manifest keeps unknown future raw metadata, which this function cannot start.
func DecodeAutomaticRestore(raw json.RawMessage) (*AutomaticRestorePlan, error) {
	if len(raw) == 0 || len(raw)+1 > proto.MaxFrame {
		return nil, automaticRestoreError()
	}
	fields, err := api.JSONObjectFields(raw, "version", "snapshot", "primary_wanted", "primary_status", "members")
	if err != nil || len(fields) != 5 {
		return nil, automaticRestoreError()
	}
	for _, name := range []string{"version", "snapshot", "primary_wanted", "primary_status", "members"} {
		if len(fields[name]) == 0 || bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return nil, automaticRestoreError()
		}
	}
	statusFields, err := api.JSONObjectFields(fields["primary_status"], "state", "since", "source", "exit_code", "signal", "oom_killed", "reason", "full_screen", "claimed")
	if err != nil {
		return nil, automaticRestoreError()
	}
	for key := range statusFields {
		if !slices.Contains([]string{"state", "since", "source", "exit_code", "signal", "oom_killed", "reason", "full_screen", "claimed"}, key) {
			return nil, automaticRestoreError()
		}
	}
	var plan AutomaticRestorePlan
	if json.Unmarshal(raw, &plan) != nil || plan.Version != 1 || plan.MemberIDs == nil || plan.Snapshot.Check(plan.Snapshot.Primary, proto.MaxFrame) != nil {
		return nil, automaticRestoreError()
	}
	for key := range fields {
		if !slices.Contains([]string{"version", "snapshot", "primary_wanted", "primary_status", "members"}, key) {
			return nil, automaticRestoreError()
		}
	}
	if !slices.Contains([]string{StateStarting, StateWorking, StateIdle, StateNeedsInput, StateExited, StateErrored, StateLost, StateStopped}, plan.PrimaryStatus.State) {
		return nil, automaticRestoreError()
	}
	if !plan.PrimaryWanted && len(plan.MemberIDs) == 0 {
		return nil, automaticRestoreError()
	}
	for index, id := range plan.MemberIDs {
		if !api.ValidSessionID(id) || id == plan.Snapshot.Primary || !slices.Contains(plan.Snapshot.Members, id) || index > 0 && plan.MemberIDs[index-1] >= id {
			return nil, automaticRestoreError()
		}
	}
	return &plan, nil
}

// ValidateAutomaticRestore checks the complete protected membership without
// reacquiring the launcher's retained primary lease.
func ValidateAutomaticRestore(m *Manifest) (*AutomaticRestorePlan, error) {
	if m.path != "" || m.SandboxOf != "" {
		return nil, automaticRestoreError()
	}
	plan, err := DecodeAutomaticRestore(m.AutomaticRestore)
	if err != nil {
		return nil, err
	}
	if plan.Snapshot.Primary != m.ID {
		return nil, automaticRestoreError()
	}
	if plan.PrimaryWanted == m.Ended() {
		return nil, automaticRestoreError()
	}
	if err := CheckSandboxSnapshotLocked(m.ID, m.ID, &plan.Snapshot); err != nil {
		return nil, err
	}
	members, err := SandboxMembers(m.ID)
	if err != nil {
		return nil, err
	}
	for _, member := range members {
		if slices.Contains(plan.MemberIDs, member.ID) {
			if member.Ended() || member.Restart.Policy != "resume" {
				return nil, automaticRestoreError()
			}
			if _, err := ResumeSandboxMember(member, m); err != nil {
				return nil, err
			}
		}
	}
	return plan, nil
}

func automaticRestoreError() error {
	return UserError("restore_plan", "the protected automatic restore plan is unsupported or changed", "Use the current helper and recheck the sandbox's saved membership before restoring.")
}
