package client

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

func describeRootAccountStep(id RootStepID, user string) (RootStep, bool) {
	switch id {
	case RootLauncher:
		return RootStep{ID: id, Line: "Install the signed root-owned launcher and one sudoers line that allows no arguments", Reason: "Allow this login account to start sandbox VMMs under slot accounts.", Undo: "Restore this user's earlier sudoers file first. The final user restores the earlier shared launcher."}, true
	case RootSlots:
		return RootStep{ID: id, Line: "Create 16 system slot accounts and a root-owned read-only runtime store", Reason: "Keep VMMs outside the login account and keep one slot outside another's files.", Undo: "Remove this user's sudoers rule first, refuse live slots, then remove only accounts created by this root step."}, true
	}
	return RootStep{}, false
}

func rootAccountsUndo(user string, uid int) (string, error) {
	replace := strings.NewReplacer("@BRAND@", brand.Name, "@USER@", user, "@UID@", strconv.Itoa(uid))
	read := func(name string) (string, error) {
		data, err := rootTemplates.ReadFile("rootstep/" + name + ".sh")
		return replace.Replace(string(data)), err
	}
	undo, err := read("undo")
	if err != nil {
		return "", err
	}
	launcher, err := read("undo-launcher")
	if err != nil {
		return "", err
	}
	slots, err := read("undo-slots")
	if err != nil {
		return "", err
	}
	const anchor = "        kvm-acl)"
	if strings.Count(undo, anchor) != 1 {
		return "", rootStepError("the fixed undo template has no unique account-step insertion point")
	}
	cases := "        launcher)\n" + launcher + "\n            ;;\n        slots)\n" + slots + "\n            ;;\n"
	return strings.Replace(undo, anchor, cases+anchor, 1), nil
}

func (p *rootAccountPreparation) build() (RootScript, error) {
	if p == nil {
		return RootScript{}, grantPurposeError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buildLocked()
}

func (p *rootAccountPreparation) buildLocked() (RootScript, error) {
	if p.closed || !p.staged || p.process != nil || p.starting || p.conn == nil || p.request == nil || !rootUserRE.MatchString(p.user) || p.user == "root" || p.uid <= 0 || !supply.IsHex64(p.item.SHA256) || p.item.Kind != supply.KindLaunch || p.item.Name != "launch" || p.item.Arch != p.arch || p.item.Size <= 0 || p.item.Size > 256<<20 {
		return RootScript{}, rootStepError("the account root script has no actual original launcher preparation")
	}
	result := RootScript{User: p.user, accounts: p, NitroSlots: p.nitroSlots}
	seen := map[RootStepID]bool{}
	for _, id := range p.ids {
		step, ok := describeRootAccountStep(id, p.user)
		if !ok {
			step, ok = describeRootStep(id, p.user, p.uid)
		}
		if !ok || seen[id] {
			return RootScript{}, rootStepError("the account root script has an unknown or repeated step")
		}
		if id == RootNitroSlots {
			step.Line = fmt.Sprintf("Write %d to /sys/class/misc/nitro_enclaves/pcie_slots", p.nitroSlots)
		}
		seen[id] = true
		result.Steps = append(result.Steps, step)
	}
	if !seen[RootLauncher] || !seen[RootSlots] {
		return RootScript{}, rootStepError("the separate-account preparation needs both fixed account steps")
	}
	undo, err := rootAccountsUndo(p.user, p.uid)
	if err != nil {
		return RootScript{}, err
	}
	result.Undo = undo
	replace := strings.NewReplacer("@BRAND@", brand.Name, "@USER@", p.user, "@UID@", strconv.Itoa(p.uid), "@LAUNCH_SHA256@", p.item.SHA256, "@LAUNCH_SIZE@", strconv.FormatInt(p.item.Size, 10), "@NITRO_SLOTS@", strconv.Itoa(p.nitroSlots))
	read := func(name string) (string, error) {
		data, err := rootTemplates.ReadFile("rootstep/" + name + ".sh")
		return replace.Replace(string(data)), err
	}
	common, err := read("common")
	if err != nil {
		return RootScript{}, err
	}
	var text strings.Builder
	text.WriteString(common)
	text.WriteString("cat > \"$d/undo.sh.new\" <<'ROOT_STEP_UNDO'\n")
	text.WriteString(undo)
	text.WriteString("ROOT_STEP_UNDO\nchmod 0700 \"$d/undo.sh.new\"\nmv -f \"$d/undo.sh.new\" \"$d/undo.sh\"\n")
	if seen[RootSubuid] {
		allocator, err := read("subid-allocate")
		if err != nil {
			return RootScript{}, err
		}
		text.WriteString("cat > \"$d/subid-allocate.sh.new\" <<'ROOT_SUBID_ALLOCATOR'\n")
		text.WriteString(allocator)
		text.WriteString("ROOT_SUBID_ALLOCATOR\nchmod 0700 \"$d/subid-allocate.sh.new\"\nmv -f \"$d/subid-allocate.sh.new\" \"$d/subid-allocate.sh\"\n")
	}
	for i, step := range result.Steps {
		body, err := read(string(step.ID))
		if err != nil {
			return RootScript{}, err
		}
		fmt.Fprintf(&text, "printf '%%s\\n' %s\n", ShellQuote(fmt.Sprintf("%d/%d %s", i+1, len(result.Steps), step.ID)))
		text.WriteString(body)
	}
	text.WriteString("printf '%s\\n' __ROOT_STEP_DONE__\n")
	result.Text = text.String()
	hash := sha256.Sum256([]byte(result.Text))
	result.SHA256 = hex.EncodeToString(hash[:])
	return result, nil
}

// canonical is a pure rebuild from the SAME private preparation. The caller
// separately rechecks its actual R/Source/native staging before every effect.
func (p *rootAccountPreparation) canonical(probe api.CapProbe, script RootScript) (RootScript, error) {
	if p == nil || script.accounts != p || probe.OS != "linux" || probe.User != p.user || probe.UID != p.uid || probe.Arch != p.arch {
		return RootScript{}, grantPurposeError()
	}
	canonical, err := p.build()
	if err != nil {
		return RootScript{}, err
	}
	if !reflect.DeepEqual(canonical, script) {
		return RootScript{}, rootStepError("the account root script differs from its original fixed staged plan")
	}
	canonical.Steps = slices.Clone(canonical.Steps)
	return canonical, nil
}
