//go:build darwin

package join

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/macfacts"
)

func (e *macExecutor) configureMacSSH(ctx context.Context, account *macAccount) error {
	key, err := macKeyLine(e.plan, e.ledger.GuestPort)
	if err != nil {
		return err
	}
	if err := e.installFile("outer-key", filepath.Join(account.Home, ".ssh", "authorized_keys"), []byte(key), 0444); err != nil {
		return err
	}
	drop, err := macDropIn(account.Name, e.ledger.GuestPort)
	if err != nil {
		return err
	}
	config, _, err := macReadFile("/private/etc/ssh/sshd_config", 1<<20)
	if err != nil {
		return err
	}
	if !macHasSSHDInclude(string(config)) {
		return macFailure("join_mac_sshd", "the Mac sshd has no active global drop-in include")
	}
	if err := macRootDir("/private/etc/ssh/sshd_config.d", 0755); err != nil {
		return err
	}
	path := filepath.Join("/private/etc/ssh/sshd_config.d", "00-"+brand.Name+"-"+account.Name+".conf")
	if err := e.installFile("sshd", path, []byte(drop), 0644); err != nil {
		return err
	}
	if _, err := macCommand(ctx, nil, nil, "/usr/sbin/sshd", "-t"); err != nil {
		return err
	}
	effective, err := macCommand(ctx, nil, nil, "/usr/sbin/sshd", "-T", "-C", "user="+account.Name+",host=localhost,addr=127.0.0.1")
	if err != nil {
		return err
	}
	if err := verifyMacSSHD(string(effective), account.Name, e.ledger.GuestPort); err != nil {
		if undoErr := e.undoFile(e.step("sshd")); undoErr != nil {
			return fmt.Errorf("retain the changed Mac SSH policy after verification failed: %w", undoErr)
		}
		e.step("sshd").State = "undone"
		_ = e.save()
		return err
	}
	// Existing Remote Login stays enabled by its owner. Reload only that
	// original system service after syntax and every effective value pass.
	if _, err := e.intent("sshd-reload", ""); err != nil {
		return err
	}
	if err := macReloadSSHD(ctx); err != nil {
		return err
	}
	return e.applied("sshd-reload")
}

func (e *macExecutor) launchDetails(owner *macAccount) (label, domain, path string) {
	label = "io.lima-vm.daemon." + e.ledger.Instance
	domain = "system"
	path = filepath.Join("/Library/LaunchDaemons", label+".plist")
	if e.ledger.Options.StartMode == "user-agent" {
		label = "io.lima-vm.autostart." + e.ledger.Instance
		domain = "gui/" + strconv.Itoa(owner.UID)
		// A global agent is root-owned, while LimitLoadToUserName below
		// selects only the explicitly approved existing Mac user.
		path = filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "agent.plist")
	}
	return
}

func (e *macExecutor) startMacVM(ctx context.Context, account *macAccount) error {
	owner, err := e.vmOwner(ctx, account)
	if err != nil {
		return err
	}
	if err := e.checkMacProfile(ctx, owner); err != nil {
		return err
	}
	if step := e.step("launch"); step != nil && step.State == "applied" {
		item, err := e.observeMacVM(ctx, owner)
		if err != nil {
			return err
		}
		if item.Status == "Running" {
			return nil
		}
	}
	// First prove the pinned native VM can start. This is an actual Lima
	// operation, not a synthetic ready record or a framework capability guess.
	boot, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	text, startErr := e.limaCommand(boot, owner, "start", "--tty=false", e.ledger.Instance)
	if startErr != nil {
		refused := strings.Contains(string(text), "nested virtualization is not supported on this device") || strings.Contains(string(text), "nested virtualization requires macOS 15 or newer")
		if !refused || e.ledger.NestedDisabled {
			return fmt.Errorf("start the original Mac VM: %w", startErr)
		}
		if _, err := e.intent("nested-off", ""); err != nil {
			return err
		}
		if _, err := e.limaCommand(boot, owner, "edit", "--tty=false", "--set", ".nestedVirtualization = false", e.ledger.Instance); err != nil {
			return err
		}
		e.ledger.NestedDisabled = true
		if err := e.captureMacProfile(boot, owner, "profile-no-nesting"); err != nil {
			return err
		}
		if err := e.applied("nested-off"); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "This Mac cannot run nested virtualisation. The Linux VM remains available for process and gvisor walls.")
		if _, err := e.limaCommand(boot, owner, "start", "--tty=false", e.ledger.Instance); err != nil {
			return err
		}
	}
	if _, err := e.observeMacVM(boot, owner); err != nil {
		return err
	}
	// Stop this exact newly prepared VM before handing it to launchd. An
	// already-running VM would make a foreground start exit successfully.
	if _, err := e.limaCommand(boot, owner, "stop", "--tty=false", e.ledger.Instance); err != nil {
		return err
	}
	observed, err := e.observeMacVM(boot, owner)
	if err != nil || observed.Status != "Stopped" {
		return macFailure("join_mac_stop", "the original VM did not stop before the startup handoff")
	}
	if err := e.publishMacGuard(boot, owner); err != nil {
		return err
	}
	label, domain, path := e.launchDetails(owner)
	plist, err := macPlist(e.ledger.Account, e.ledger.Instance, e.limaBinary(), e.ledger.LimaHome, owner.Home, e.ledger.Options.StartMode, owner.UID)
	if err != nil {
		return err
	}
	if e.ledger.Options.StartMode == "user-agent" {

		fmt.Fprintln(e.out, "WARNING: the Mac's user must stay logged in. Anyone at its unlocked screen can read this VM's disk and agent logins.")
	}
	if err := e.installFile("launch-plist", path, plist, 0644); err != nil {
		return err
	}
	if e.ledger.Options.StartMode == "user-agent" {
		if err := e.installMacAgentLink(ctx, owner, path, label); err != nil {
			return err
		}
	}
	if _, err := e.intent("launch", domain+"/"+label); err != nil {
		return err
	}
	if _, err := macCommand(boot, nil, nil, "/bin/launchctl", "bootstrap", domain, path); err != nil {
		return &code.Error{Code: "join_mac_launch", Message: "the original Mac startup job did not load", Hint: "Keep the ledger and inspect launchd. If the documented never-logged-in account test fails, undo this join and use --start-mode user-agent --agent-user with the Mac's existing user."}
	}
	loaded, err := macCommand(boot, nil, nil, "/bin/launchctl", "print", domain+"/"+label)
	if err != nil {
		return err
	}
	if err := verifyMacLoadedJob(string(loaded), e.ledger.Account, owner.UID); err != nil {
		return err
	}
	for {
		observed, err := e.observeMacVM(boot, owner)
		if err == nil && observed.Status == "Running" {
			break
		}
		select {
		case <-boot.Done():
			return macFailure("join_mac_launch", "the original startup job did not make its VM ready")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err := e.checkMacProfile(boot, owner); err != nil {
		return err
	}
	return e.applied("launch")
}

func (e *macExecutor) changeACSleep(ctx context.Context) error {
	data, err := macCommand(ctx, nil, nil, "/usr/bin/pmset", "-g", "custom")
	if err != nil {
		return err
	}
	value, known := macfacts.ParseACSleep(string(data))
	if !known {
		return macFailure("join_mac_power", "the existing AC sleep setting could not be read")
	}
	if !e.ledger.ACSleepRecorded {
		e.ledger.OriginalACSleep = value
		e.ledger.ACSleepRecorded = true
		if _, err := e.intent("ac-sleep", ""); err != nil {
			return err
		}
		if err := e.save(); err != nil {
			return err
		}
	}
	if _, err := macCommand(ctx, nil, nil, "/usr/bin/pmset", "-c", "sleep", "0"); err != nil {
		return err
	}
	data, err = macCommand(ctx, nil, nil, "/usr/bin/pmset", "-g", "custom")
	if err != nil {
		return err
	}
	if current, known := macfacts.ParseACSleep(string(data)); !known || current != 0 {
		return macFailure("join_mac_power", "the AC sleep change was not confirmed")
	}
	return e.applied("ac-sleep")
}

func (e *macExecutor) publishMacFacts(ctx context.Context, account *macAccount) error {
	owner, err := e.vmOwner(ctx, account)
	if err != nil {
		return err
	}
	label, domain, _ := e.launchDetails(owner)
	data, err := json.Marshal(macfacts.LocalStatusV1{Version: 1, Account: account.Name, UID: account.UID, StartMode: e.ledger.Options.StartMode, LaunchTarget: domain + "/" + label})
	if err != nil {
		return err
	}
	return e.installFile("facts-status", filepath.Join(filepath.Dir(macHelperPath(account.Name)), "facts.json"), append(data, '\n'), 0444)
}

func verifyMacLoadedJob(text, account string, uid int) error {
	program := macHelperPath(account)
	foundProgram := false
	var args []string
	inArguments := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "program = ") {
			if foundProgram || strings.Trim(strings.TrimPrefix(trimmed, "program = "), `"`) != program {
				return macFailure("join_mac_launch", "the native launch label now runs another program")
			}
			foundProgram = true
		}
		if trimmed == "arguments = {" {
			if inArguments || len(args) != 0 {
				return macFailure("join_mac_launch", "the native launch job has ambiguous arguments")
			}
			inArguments = true
			continue
		}
		if inArguments {
			if trimmed == "}" {
				inArguments = false
				continue
			}
			args = append(args, strings.Trim(trimmed, `"`))
		}
	}
	want := []string{program, "host", "_mac-vm-start", "--account", account}
	if !foundProgram || inArguments || len(args) != len(want) {
		return macFailure("join_mac_launch", "the actual native startup job cannot be matched to this original join")
	}
	for i := range want {
		if args[i] != want[i] {
			return macFailure("join_mac_launch", "the native startup arguments changed after this join")
		}
	}
	if uid <= 0 {
		return macFailure("join_mac_launch", "the original launch owner is invalid")
	}
	return nil
}
