// Package join plans and executes local machine enrolment. It listens on no
// port and never connects back to the laptop.
package join

import (
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
)

// Change explains a conditional change and its undo before consent.
type Change struct{ Action, Undo string }

// LinuxPlan retains the original checked invite and local connection choices.
// Its private fields cannot be supplied by a reply or replayed ledger.
type LinuxPlan struct {
	invite    code.Invite
	account   string
	addresses []string
	port      uint16
}

// NewLinuxPlan checks the invite before any privilege or file change.
func NewLinuxPlan(invite code.Invite, account string, addresses []string, port uint16, now time.Time) (*LinuxPlan, error) {
	encoded, err := code.EncodeInvite(invite)
	if err != nil {
		return nil, err
	}
	if _, err = code.DecodeInvite(encoded, now); err != nil {
		return nil, err
	}
	if !code.ValidAccount(account) {
		return nil, &code.Error{Code: "join_account", Message: "the join needs a non-root account of at most 32 lower-case letters, digits, dashes or underscores", Hint: "Use --account with a name that starts with a letter or underscore."}
	}
	if err = code.ValidateConnection(account, addresses, port); err != nil {
		return nil, err
	}
	return &LinuxPlan{invite: invite, account: account, addresses: append([]string(nil), addresses...), port: port}, nil
}

// Account is the checked account whose local changes the owner authorises.
func (p *LinuxPlan) Account() string { return p.account }

// Changes lists all possible changes, including probe-selected root changes.
func (p *LinuxPlan) Changes() []Change {
	return []Change{
		{"Create account " + p.account + " with a private group, a home and a locked password, if absent.", "Stop ALL processes of only the original join-created account, then remove its home, account and private group."},
		{"Write /etc/ssh/sshd_config.d/00-" + brand.Name + ".conf for this account; test its effective values and reload sshd.", "Restore the previous drop-in, test it and reload sshd."},
		{"Add the laptop's public key with restrict,pty,port-forwarding.", "Restore the previous authorised keys if this join's file is unchanged."},
		{"Enable linger for this account, if needed.", "Undo only this join's recorded linger creation."},
		{"Probe as " + p.account + " with runuser, then run the fixed missing sandbox root steps: KVM ACL/group, subordinate IDs, io_uring and vsock protection.", "Undo only the original root journal entries added by this join, in reverse order."},
		{"Keep a root-owned undo ledger, helper and leave script in /var/lib/" + brand.Name + "/join/.", "Remove the completed join's own ledger after every undo succeeds."},
	}
}

// PlanText is the complete change and undo list shown before the single answer.
func (p *LinuxPlan) PlanText() string {
	out := ""
	for n, c := range p.Changes() {
		out += fmt.Sprintf("%d. %s\n   Undo: %s\n", n+1, c.Action, c.Undo)
	}
	return out
}
