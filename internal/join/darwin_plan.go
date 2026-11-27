package join

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/supply"
)

// DarwinOptions are original local choices, not paths or runtime authority.
// The closed root stdin envelope carries only these checked dimensions.
type DarwinOptions struct {
	CPUs               int
	MemoryBytes        int64
	AllowACSleepChange bool
	StartMode          string
	AgentUser          string
}

// DarwinPlan retains the checked invite and exact client release data. Root
// construction reloads that release from the actual executing client binary.
type DarwinPlan struct {
	invite     code.Invite
	account    string
	addresses  []string
	port       uint16
	options    DarwinOptions
	lima       supply.Item
	image      supply.Item
	release    string
	runtime    string
	advisories []supply.Advisory
}

// NewDarwinPlan checks every public field before any native effect. Pins must
// come from the actual root client's bundled release, never a reply or stdin.
func NewDarwinPlan(invite code.Invite, account string, addresses []string, port uint16, options DarwinOptions, release supply.Manifest, now time.Time) (*DarwinPlan, error) {
	text, err := code.EncodeInvite(invite)
	if err != nil {
		return nil, err
	}
	checked, err := code.DecodeInvite(text, now)
	if err != nil {
		return nil, err
	}
	invite = checked
	if !code.ValidAccount(account) {
		return nil, macFailure("join_account", "the Mac join needs a checked dedicated non-root account")
	}
	probe := code.Reply{Nonce: invite.Nonce, Account: account, Addresses: addresses, Port: port, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	if options.StartMode == "" {
		options.StartMode = "daemon"
	}
	if options.StartMode != "daemon" && options.StartMode != "user-agent" || options.CPUs < 0 || options.CPUs > 1024 || options.MemoryBytes < 0 || options.MemoryBytes > 1<<60 {
		return nil, macFailure("join_mac_options", "the Mac VM dimensions or startup mode are invalid")
	}
	if options.StartMode == "user-agent" {
		if !code.ValidAccount(options.AgentUser) || options.AgentUser == account {
			return nil, macFailure("join_mac_agent", "the user launch agent needs the original existing Mac user's account")
		}
	} else if options.AgentUser != "" {
		return nil, macFailure("join_mac_agent", "a system daemon cannot also select a Mac user launch agent")
	}
	lima, image, err := supply.MacRuntimeItemsV1(release)
	if err != nil {
		return nil, err
	}
	return &DarwinPlan{invite: invite, account: account, addresses: append([]string(nil), addresses...), port: port, options: options, lima: lima, image: image, release: release.Release, runtime: release.Runtime, advisories: append([]supply.Advisory(nil), release.Advisories...)}, nil
}

// Account is the dedicated non-root Mac account authorised by the local user.
func (p *DarwinPlan) Account() string { return p.account }

// PlanText describes every possible change, its scope and its honest undo.
func (p *DarwinPlan) PlanText() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Create the hidden standard account %s with SecureToken disabled and a random password that is not retained.\nUndo: remove only this join's original GeneratedUID and home.\n", p.account)
	fmt.Fprintf(&out, "Install this release's Lima %s and its pinned Linux VM image in root-owned folders.\nUndo: remove only unchanged installed objects recorded by this join.\n", p.lima.Version)
	fmt.Fprintf(&out, "Create a plain vz VM with no mounts, port forwarding, containerd or access to the Mac's loopback. Use %s CPUs and %s memory, each below half the Mac's total.\nUndo: stop and delete only this original VM.\n", macDimension(p.options.CPUs), macMemory(p.options.MemoryBytes))
	fmt.Fprintln(&out, "Grant the laptop's key access only to the VM's fixed loopback SSH port and the closed facts-v1 action. Verify sshd's effective policy. Remote Login must already allow the account; this command never enables it.")
	if p.options.StartMode == "daemon" {
		fmt.Fprintln(&out, "Write a root-owned system launch daemon with UserName, LIMA_HOME, KeepAlive and a 60-second exit timeout. Undo: unload and remove only its original recorded plist.")
	} else {
		fmt.Fprintf(&out, "Use a launch agent of %s. WARNING: this user must stay logged in, and anyone at the unlocked Mac can read the VM disk and its agent logins. Undo: unload that original agent and remove its original VM.\n", p.options.AgentUser)
	}
	if p.options.AllowACSleepChange {
		fmt.Fprintln(&out, "Set pmset -c sleep 0, on AC power only. Undo: restore the recorded AC sleep value if this join's value is still applied. Battery settings stay under the owner's control.")
	} else {
		fmt.Fprintln(&out, "Keep the current AC sleep setting. A non-zero value is reported by host doctor.")
	}
	fmt.Fprintf(&out, "Keep the root-owned ahead ledger and leave helper under /var/db/%s/join. Keep the lid open, set Charge Limit to 80%% yourself, and provide good air flow. A restart requires local FileVault unlock.\n", brand.Name)
	return out.String()
}

func macDimension(value int) string {
	if value == 0 {
		return "automatic"
	}
	return fmt.Sprint(value)
}

func macMemory(value int64) string {
	if value == 0 {
		return "automatic"
	}
	return fmt.Sprintf("%d bytes of", value)
}

func macFailure(codeName, message string) error {
	return &code.Error{Code: codeName, Message: message, Hint: "Keep the Mac's root-owned leave ledger and ask its owner to check the recorded change."}
}
