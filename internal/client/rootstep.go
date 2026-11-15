package client

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// RootStepID names a supported, optional privileged preparation step.
type RootStepID string

// The seven initial root steps. Later drivers add their own steps.
const (
	RootKVMACL       RootStepID = "kvm-acl"
	RootKVMGroup     RootStepID = "kvm-group"
	RootLinger       RootStepID = "linger"
	RootDelegateIO   RootStepID = "delegate-io"
	RootSubuid       RootStepID = "subuid"
	RootIOUringOff   RootStepID = "io-uring-off"
	RootVsockModules RootStepID = "vsock-modules"
)

// RootStep describes every privileged change before the user authorises it.
type RootStep struct {
	ID          RootStepID
	Line        string
	Reason      string
	Undo        string
	MachineWide bool
}

// RootScript is the exact text displayed, hashed and passed to sudo.
type RootScript struct {
	User   string
	Steps  []RootStep
	Text   string
	Undo   string
	SHA256 string
}

//go:embed rootstep/*.sh
var rootTemplates embed.FS

var rootUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func rootStepError(message string) error {
	return &UserError{Msg: message, Hint: "Run the sandbox probe again, then ask the machine's owner to check the account and root steps.", Code: "root_step", Kind: KindRefused}
}

// BuildRootScript builds only fixed templates. It accepts no arbitrary command,
// path, runtime or host text. nitroSlots is reserved for the later Nitro driver.
func BuildRootScript(user string, uid int, ids []RootStepID, nitroSlots int) (RootScript, error) {
	if !rootUserRE.MatchString(user) || user == "root" || uid <= 0 {
		return RootScript{}, rootStepError("the root step needs a non-root account with a valid user name and uid")
	}
	if nitroSlots != 0 {
		return RootScript{}, rootStepError("this client does not support Nitro root steps")
	}
	rs := RootScript{User: user}
	seen := map[RootStepID]bool{}
	for _, id := range ids {
		step, ok := describeRootStep(id, user, uid)
		if !ok || seen[id] {
			return RootScript{}, rootStepError(fmt.Sprintf("the root step %q is unknown or repeated", CleanLine(string(id))))
		}
		seen[id] = true
		rs.Steps = append(rs.Steps, step)
	}
	replace := strings.NewReplacer("@BRAND@", brand.Name, "@USER@", user, "@UID@", strconv.Itoa(uid))
	read := func(name string) string {
		data, _ := rootTemplates.ReadFile("rootstep/" + name + ".sh")
		text := replace.Replace(string(data))
		return text
	}
	rs.Undo = read("undo")
	var script strings.Builder
	script.WriteString(read("common"))
	script.WriteString("cat > \"$d/undo.sh.new\" <<'ROOT_STEP_UNDO'\n")
	script.WriteString(rs.Undo)
	script.WriteString("ROOT_STEP_UNDO\nchmod 0700 \"$d/undo.sh.new\"\nmv -f \"$d/undo.sh.new\" \"$d/undo.sh\"\n")
	if seen[RootSubuid] {
		script.WriteString("cat > \"$d/subid-allocate.sh.new\" <<'ROOT_SUBID_ALLOCATOR'\n")
		script.WriteString(read("subid-allocate"))
		script.WriteString("ROOT_SUBID_ALLOCATOR\nchmod 0700 \"$d/subid-allocate.sh.new\"\nmv -f \"$d/subid-allocate.sh.new\" \"$d/subid-allocate.sh\"\n")
	}
	for i, step := range rs.Steps {
		fmt.Fprintf(&script, "printf '%%s\\n' %s\n", ShellQuote(fmt.Sprintf("%d/%d %s", i+1, len(rs.Steps), step.ID)))
		script.WriteString(read(string(step.ID)))
	}
	script.WriteString("printf '%s\\n' __ROOT_STEP_DONE__\n")
	rs.Text = script.String()
	sum := sha256.Sum256([]byte(rs.Text))
	rs.SHA256 = hex.EncodeToString(sum[:])
	return rs, nil
}

func describeRootStep(id RootStepID, user string, uid int) (RootStep, bool) {
	u := ShellQuote(user)
	delegate := fmt.Sprintf("/etc/systemd/system/user@%d.service.d/50-%s-delegate.conf", uid, brand.Name)
	steps := map[RootStepID]RootStep{
		RootKVMACL:       {ID: id, Line: "setfacl -m " + ShellQuote("u:"+user+":rw") + " /dev/kvm", Reason: "Open /dev/kvm now. The ACL survives a udev change event.", Undo: "setfacl -x " + ShellQuote("u:"+user) + " /dev/kvm"},
		RootKVMGroup:     {ID: id, Line: "usermod -aG kvm " + u, Reason: "Keep KVM access after a reboot. Running user managers keep their current groups.", Undo: "gpasswd -d " + u + " kvm"},
		RootLinger:       {ID: id, Line: "loginctl enable-linger " + u, Reason: "Keep sessions after logout.", Undo: "loginctl disable-linger " + u},
		RootDelegateIO:   {ID: id, Line: "Write " + delegate + " with Delegate=cpu cpuset io memory pids, then systemctl daemon-reload", Reason: "Delegate disk I/O limits to this user. Re-execute the user manager afterwards. Running sessions continue.", Undo: "Restore " + delegate + ", then systemctl daemon-reload"},
		RootSubuid:       {ID: id, Line: "Lock passwd, group, subuid and subgid, then add " + user + ":<free-start>:65536 to both subordinate ID files", Reason: "Give jails 65,536 subordinate IDs. Check both subordinate files and all local account IDs first.", Undo: "usermod --del-subuids <recorded-range> --del-subgids <recorded-range> " + u},
		RootIOUringOff:   {ID: id, Line: "Write kernel.io_uring_disabled = 2 to /etc/sysctl.d/50-" + brand.Name + ".conf, then sysctl -p that file", Reason: "Close io_uring to every process on the machine.", Undo: "Remove this user's reference. The final undo restores the original file and sysctl value.", MachineWide: true},
		RootVsockModules: {ID: id, Line: "Write /etc/modprobe.d/50-" + brand.Name + "-vsock.conf to block vsock_loopback, vmw_vsock_virtio_transport and vhost_vsock", Reason: "Block unused vsock modules. Already loaded modules stay loaded until a reboot.", Undo: "Remove this user's reference. The final undo restores the original modprobe file.", MachineWide: true},
	}
	s, ok := steps[id]
	return s, ok
}

// RootStepsFor selects steps from capability facts alone. A present device is
// insufficient to offer KVM changes without an actual microvm driver probe.
func RootStepsFor(p api.CapProbe, r api.RuntimeStatus, delegateIO bool, nitroSlots int) []RootStepID {
	if p.OS != "linux" || p.UID <= 0 || !rootUserRE.MatchString(p.User) || p.User == "root" {
		return nil
	}
	var out []RootStepID
	micro := slices.ContainsFunc(p.Walls, func(w api.WallProbe) bool { return w.Level == "microvm" && w.Driver != "nitro" })
	if micro && p.KVM.Present {
		if !p.KVM.Usable && p.HasSetfacl {
			out = append(out, RootKVMACL)
		}
		if !p.KVM.InGroup && p.KVM.Group == "kvm" {
			out = append(out, RootKVMGroup)
		}
	}
	if !p.Linger {
		out = append(out, RootLinger)
	}
	if delegateIO && !slices.Contains(p.Delegated, "io") {
		out = append(out, RootDelegateIO)
	}
	if !p.Subuid && p.HasNewuidmap {
		out = append(out, RootSubuid)
	}
	if p.IOURingDisabled < 2 {
		out = append(out, RootIOUringOff)
	}
	_, after := api.LevelsFor(p, r)
	if !p.VsockBlocked && after == "microvm" {
		out = append(out, RootVsockModules)
	}
	return out
}

// RootStepMode names how the client handles the optional root script.
type RootStepMode string

// Root script modes, shared by the CLI and launcher.
const (
	RootRun   RootStepMode = "run"
	RootPrint RootStepMode = "print"
	RootSkip  RootStepMode = "skip"
)

// DefaultRootStepMode keeps passwords off accounts that may have run a sandbox.
// Both the laptop's identity-specific history and the fresh probe must be empty.
func DefaultRootStepMode(tier Tier, localStarted bool, p api.CapProbe, yes bool) RootStepMode {
	if yes || tier != TierOwn && tier != TierThrowaway || localStarted || p.Started != 0 {
		return RootPrint
	}
	return RootRun
}

// ParseRootStepMode checks the explicit CLI choice.
func ParseRootStepMode(value string) (RootStepMode, error) {
	mode := RootStepMode(value)
	if mode != RootRun && mode != RootPrint && mode != RootSkip {
		return "", &UserError{Msg: fmt.Sprintf("%q is not a root step mode", CleanLine(value)), Hint: "Use --root-step run, print or skip.", Code: "root_step_mode", Kind: KindUsage}
	}
	return mode, nil
}
