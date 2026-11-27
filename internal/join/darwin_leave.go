//go:build darwin

package join

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/macfacts"
)

func (e *macExecutor) validateMacLedger() error {
	l := e.ledger
	if l == nil || l.Version != 1 || l.Account == "" || l.Account != filepath.Base(e.dir) || l.Instance != l.Account+"-host" || l.Nonce == 0 || len(l.GUID) != 36 || len(l.KeyHash) != 64 || l.UID < 0 || l.GID < 0 || l.OriginalACSleep < 0 || l.OriginalACSleep > 1440 || l.Options.StartMode != "daemon" && l.Options.StartMode != "user-agent" {
		return macFailure("join_mac_ledger", "the original Mac join ledger has invalid fixed fields")
	}
	if l.UID > 0 {
		home := filepath.Join("/private/var", brand.Name, "hosts", l.Account)
		if l.Home != home {
			return macFailure("join_mac_ledger", "the Mac ledger names another dedicated home")
		}
		if l.Options.StartMode == "daemon" && l.LimaHome != filepath.Join(home, "lima") {
			return macFailure("join_mac_ledger", "the Mac ledger names another Lima home")
		}
		if l.Options.StartMode == "user-agent" && (l.AgentUser == "" || l.AgentUID <= 0 || l.AgentGUID == "" || !strings.HasPrefix(l.AgentHome, "/Users/") || l.LimaHome != filepath.Join(l.AgentHome, "Library/Application Support", brand.Name, "hosts", l.Account)) {
			return macFailure("join_mac_ledger", "the Mac ledger has no original launch-agent owner")
		}
	}
	public := filepath.Dir(macHelperPath(l.Account))
	expected := map[string]string{
		"helper": macHelperPath(l.Account), "leave": filepath.Join(e.dir, "leave.sh"), "facts-dispatcher": macFactsDispatcher(l.Account),
		"account": "", "home": l.Home, "runtime": e.runtimeDir(), "download-lima.tar.gz": "", "download-base.img": "",
		"lima-home": l.LimaHome, "vm": filepath.Join(l.LimaHome, l.Instance), "profile-input": filepath.Join(public, "input.yaml"),
		"profile": filepath.Join(public, "profile.yaml"), "profile-no-nesting": filepath.Join(public, "profile.yaml"),
		"outer-key": filepath.Join(l.Home, ".ssh/authorized_keys"), "sshd": filepath.Join("/private/etc/ssh/sshd_config.d", "00-"+brand.Name+"-"+l.Account+".conf"),
		"sshd-reload": "", "nested-off": "", "ac-sleep": "", "guard-policy": filepath.Join(public, "guard.json"), "facts-status": filepath.Join(public, "facts.json"),
	}
	owner := &macAccount{UID: l.UID, Home: l.Home}
	if l.Options.StartMode == "user-agent" {
		owner.UID, owner.Home = l.AgentUID, l.AgentHome
	}
	label, domain, plist := e.launchDetails(owner)
	expected["launch"], expected["launch-plist"] = domain+"/"+label, plist
	expected["agent-link"] = filepath.Join(l.AgentHome, "Library/LaunchAgents", label+".plist")
	seen := map[string]bool{}
	for _, step := range l.Steps {
		path, known := expected[step.ID]
		if !known || seen[step.ID] || step.Path != path || step.State != "intent" && step.State != "applied" && step.State != "undone" {
			return macFailure("join_mac_ledger", "the Mac ledger contains an unknown effect, path or state")
		}
		seen[step.ID] = true
	}
	return nil
}

// LeaveDarwin first selects the original root-owned client implementation,
// then undoes only that join's native anchored effects, newest first.
func LeaveDarwin(ctx context.Context, account string, out io.Writer) error {
	e, close, err := openMacExecutor(account, out, false)
	if err != nil {
		return err
	}
	defer close()
	ledger, err := readMacLedger(e.dir)
	if err != nil {
		return err
	}
	e.ledger = ledger
	if err := e.validateMacLedger(); err != nil {
		return err
	}
	helper := e.step("helper")
	if helper == nil || !helper.After.Exists {
		return macFailure("join_mac_leave", "the Mac leave has no original retained helper object")
	}
	implementation := macHelperPath(account)
	_, actual, err := macReadFile(implementation, 256<<20)
	if errors.Is(err, os.ErrNotExist) || err == nil && !actual.Exists {
		implementation = filepath.Join(e.dir, "anchors", "helper-after")
		_, actual, err = macReadFile(implementation, 256<<20)
	}
	if err != nil || actual != helper.After {
		return macFailure("join_mac_leave", "the original root-owned Mac leave implementation was replaced")
	}
	current, err := originalMacClient(ctx)
	if err != nil {
		return err
	}
	same := macDigest(current) == helper.After.SHA256
	clear(current)
	if !same {
		// Release the original namespace lock before the genuine old helper
		// reacquires it. No saved command or executable path is accepted.
		close()
		command := exec.CommandContext(ctx, implementation, "host", "_join-leave", "--account", account)
		command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
		command.Stdout, command.Stderr = e.out, e.out
		return command.Run()
	}
	if err := e.preflightMacLeave(ctx); err != nil {
		return err
	}
	for i := len(e.ledger.Steps) - 1; i >= 0; i-- {
		step := &e.ledger.Steps[i]
		if step.State == "undone" {
			continue
		}
		fmt.Fprintf(e.out, "Undo Mac join step %s\n", step.ID)
		if err := e.undoMacStep(ctx, step); err != nil {
			return err
		}
		step.State = "undone"
		if err := e.save(); err != nil {
			return err
		}
	}
	return e.finishMacLeave()
}

func (e *macExecutor) undoMacStep(ctx context.Context, step *macStep) error {
	switch step.ID {
	case "launch":
		account, err := e.checkMacAccount(ctx)
		if err != nil {
			return err
		}
		owner, err := e.vmOwner(ctx, account)
		if err != nil {
			return err
		}
		label, domain, _ := e.launchDetails(owner)
		if loaded, err := macCommand(ctx, nil, nil, "/bin/launchctl", "print", domain+"/"+label); err == nil {
			if err := verifyMacLoadedJob(string(loaded), e.ledger.Account, owner.UID); err != nil {
				return err
			}
			if _, err := macCommand(ctx, nil, nil, "/bin/launchctl", "bootout", domain+"/"+label); err != nil {
				return err
			}
		}
		return nil
	case "agent-link":
		return e.undoMacAgentLink(ctx, step)
	case "ac-sleep":
		if !e.ledger.ACSleepRecorded {
			return macFailure("join_mac_power", "the original AC sleep value is missing")
		}
		data, err := macCommand(ctx, nil, nil, "/usr/bin/pmset", "-g", "custom")
		if err != nil {
			return err
		}
		value, known := macfacts.ParseACSleep(string(data))
		if !known {
			return macFailure("join_mac_power", "the current AC sleep setting is unknown")
		}
		if value == e.ledger.OriginalACSleep {
			return nil
		}
		if value != 0 {
			return macFailure("join_mac_power", "the AC sleep value changed after this join; it was retained")
		}
		_, err = macCommand(ctx, nil, nil, "/usr/bin/pmset", "-c", "sleep", strconv.Itoa(e.ledger.OriginalACSleep))
		return err
	case "vm":
		if !e.ledger.VMRef.Exists {
			return macFailure("join_mac_vm", "a partial VM with no original directory identity was retained")
		}
		account, err := e.checkMacAccount(ctx)
		if err != nil {
			return err
		}
		owner, err := e.vmOwner(ctx, account)
		if err != nil {
			return err
		}
		actual, err := macDirectoryIdentity(filepath.Join(e.ledger.LimaHome, e.ledger.Instance), owner.UID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || actual != e.ledger.VMRef {
			return macFailure("join_mac_vm", "the original VM directory changed; no VM was deleted")
		}
		if _, err := e.limaCommand(ctx, owner, "stop", "--tty=false", e.ledger.Instance); err != nil {
			return err
		}
		observed, err := e.observeMacVM(ctx, owner)
		if err != nil || observed.Status != "Stopped" {
			return macFailure("join_mac_stop", "the original VM has no confirmed stopped state")
		}
		_, err = e.limaCommand(ctx, owner, "delete", "--force", e.ledger.Instance)
		return err
	case "lima-home":
		if !e.ledger.LimaRef.Exists {
			return macFailure("join_mac_vm", "a partial Lima home with no original identity was retained")
		}
		uid := e.ledger.UID
		if e.ledger.Options.StartMode == "user-agent" {
			uid = e.ledger.AgentUID
		}
		actual, err := macDirectoryIdentity(e.ledger.LimaHome, uid)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || actual != e.ledger.LimaRef {
			return macFailure("join_mac_vm", "the original Lima home changed; no directory was removed")
		}
		return os.RemoveAll(e.ledger.LimaHome)
	case "runtime":
		if !step.After.Exists {
			return macFailure("join_mac_runtime", "a partial unacknowledged runtime was retained")
		}
		actual, err := macTreeIdentity(e.runtimeDir())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || actual != step.After {
			return macFailure("join_mac_runtime", "the original pinned runtime changed; it was retained")
		}
		return os.RemoveAll(e.runtimeDir())
	case "home":
		info, err := macProtected(e.ledger.Home, true)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		native := info.Sys().(*syscall.Stat_t)
		if uint64(native.Dev) != e.ledger.HomeRef.Device || native.Ino != e.ledger.HomeRef.Inode {
			return macFailure("join_mac_home", "the original dedicated home changed; it was retained")
		}
		return os.RemoveAll(e.ledger.Home)
	case "account":
		return e.removeMacAccount(ctx)
	case "download-lima.tar.gz", "download-base.img", "nested-off":
		return nil
	case "sshd-reload":
		// The file effect is restored in its own following undo step.
		return nil
	case "helper", "leave", "facts-dispatcher", "profile-input", "profile", "profile-no-nesting", "outer-key", "sshd", "launch-plist", "guard-policy", "facts-status":
		if err := e.undoFile(step); err != nil {
			return err
		}
		if step.ID == "sshd" {
			if _, err := macCommand(ctx, nil, nil, "/usr/sbin/sshd", "-t"); err != nil {
				return err
			}
			return macReloadSSHD(ctx)
		}
		return nil
	default:
		return macFailure("join_mac_ledger", "the leave implementation does not know this original effect")
	}
}

func (e *macExecutor) removeMacAccount(ctx context.Context) error {
	path := "/Users/" + e.ledger.Account
	list, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Users")
	if err != nil {
		return err
	}
	if containsMacLine(string(list), e.ledger.Account) {
		identity, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", path, "GeneratedUID")
		if err != nil || !strings.Contains(strings.ToLower(string(identity)), e.ledger.GUID) {
			return macFailure("join_mac_account", "the dedicated account name was reused; it was retained")
		}
		if _, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-delete", path); err != nil {
			return err
		}
	}
	groups, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Groups")
	if err != nil {
		return err
	}
	if containsMacLine(string(groups), e.ledger.Account) {
		identity, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", "/Groups/"+e.ledger.Account, "GeneratedUID")
		if err != nil || !strings.Contains(strings.ToLower(string(identity)), e.ledger.GUID) {
			return macFailure("join_mac_account", "the dedicated group name was reused; it was retained")
		}
		users, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Users", "PrimaryGroupID")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(users), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == strconv.Itoa(e.ledger.GID) {
				return macFailure("join_mac_account", "another user now has the original private group")
			}
		}
		_, err = macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-delete", "/Groups/"+e.ledger.Account)
		return err
	}
	return nil
}

func (e *macExecutor) finishMacLeave() error {
	// Remove only fixed private bookkeeping. Unknown files retain the ledger
	// rather than turning a broad recursive delete into cleanup authority.
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "ledger.json", "leave.sh", "anchors", "downloads":
		default:
			return macFailure("join_mac_leave", "an unknown join directory entry was retained for inspection")
		}
	}
	for _, directory := range []string{"anchors", "downloads"} {
		path := filepath.Join(e.dir, directory)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := macProtected(path, true); err != nil {
			return err
		}
		files, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		if directory == "downloads" {
			allowed["lima.tar.gz"], allowed["base.img"] = true, true
		} else {
			for _, step := range e.ledger.Steps {
				allowed[step.ID+"-before"], allowed[step.ID+"-after"] = true, true
			}
			allowed["agent-link-after"] = true
		}
		for _, file := range files {
			if !allowed[file.Name()] {
				return macFailure("join_mac_leave", "unknown private bookkeeping was retained for inspection")
			}
			if file.IsDir() || file.Type()&os.ModeSymlink != 0 && file.Name() != "agent-link-after" {
				return macFailure("join_mac_leave", "unknown nested join bookkeeping was retained")
			}
			if err := os.Remove(filepath.Join(path, file.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	for _, name := range []string{"leave.sh", "ledger.json"} {
		if err := os.Remove(filepath.Join(e.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Remove(e.dir)
}

func (e *macExecutor) preflightMacLeave(ctx context.Context) error {
	l := e.ledger
	users, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Users")
	if err != nil {
		return err
	}
	if !containsMacLine(string(users), l.Account) {
		return nil
	}
	guid, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", "/Users/"+l.Account, "GeneratedUID")
	if err != nil || !strings.Contains(strings.ToLower(string(guid)), l.GUID) {
		return macFailure("join_mac_account", "the original dedicated account name was reused")
	}
	if !l.HomeRef.Exists && !l.VMRef.Exists {
		return nil
	}
	_, err = e.checkMacAccount(ctx)
	return err
}
