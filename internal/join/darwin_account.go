//go:build darwin

package join

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

func nextMacID(ctx context.Context) (int, error) {
	used := map[int]bool{}
	for _, record := range []struct{ path, key string }{{"/Users", "UniqueID"}, {"/Groups", "PrimaryGroupID"}} {
		text, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", record.path, record.key)
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(text), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) != 2 {
				return 0, macFailure("join_mac_account", "the local account ID table is malformed")
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 0 {
				return 0, macFailure("join_mac_account", "the local account ID table is invalid")
			}
			used[n] = true
		}
	}
	for id := 501; id < 65535; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, macFailure("join_mac_account", "no unused local Mac account ID is available")
}

func (e *macExecutor) ensureMacAccount(ctx context.Context) (*macAccount, error) {
	l := e.ledger
	if step := e.step("account"); step != nil && step.State == "applied" {
		account, err := e.checkMacAccount(ctx)
		if err != nil {
			return nil, err
		}
		return account, e.ensureMacHome()
	}
	text, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Users")
	if err != nil {
		return nil, err
	}
	if containsMacLine(string(text), l.Account) {
		if e.step("account") == nil {
			return nil, macFailure("join_mac_account", "the dedicated account already exists without this original root join")
		}
		account, err := e.checkMacAccount(ctx)
		if err != nil {
			return nil, err
		}
		if err := e.applied("account"); err != nil {
			return nil, err
		}
		return account, e.ensureMacHome()
	}
	if l.UID == 0 {
		id, err := nextMacID(ctx)
		if err != nil {
			return nil, err
		}
		l.UID, l.GID = id, id
		l.Home = filepath.Join("/private/var", brand.Name, "hosts", l.Account)
		l.LimaHome = filepath.Join(l.Home, "lima")
		if l.Options.StartMode == "user-agent" {
			agent, err := macReadAccount(ctx, l.Options.AgentUser)
			if err != nil {
				return nil, err
			}
			if agent.UID == l.UID || !strings.HasPrefix(agent.Home, "/Users/") {
				return nil, macFailure("join_mac_agent", "the launch agent user has no ordinary Mac home")
			}
			l.AgentUID, l.AgentUser, l.AgentGUID, l.AgentHome = agent.UID, agent.Name, agent.GUID, agent.Home
			l.LimaHome = filepath.Join(agent.Home, "Library/Application Support", brand.Name, "hosts", l.Account)
		}
		if err := e.save(); err != nil {
			return nil, err
		}
	}
	groups, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Groups")
	if err != nil {
		return nil, err
	}
	if containsMacLine(string(groups), l.Account) {
		if e.step("account") == nil {
			return nil, macFailure("join_mac_account", "a foreign group already owns the dedicated account name")
		}
		group, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", "/Groups/"+l.Account, "GeneratedUID")
		if err != nil || !strings.Contains(strings.ToLower(string(group)), l.GUID) {
			return nil, macFailure("join_mac_account", "the original dedicated group changed")
		}
	}
	if _, err := e.intent("account", ""); err != nil {
		return nil, err
	}
	groupPath := "/Groups/" + l.Account
	if _, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-create", groupPath, "GeneratedUID", l.GUID); err != nil {
		return nil, err
	}
	if _, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-create", groupPath, "PrimaryGroupID", strconv.Itoa(l.GID)); err != nil {
		return nil, err
	}
	record := "/Users/" + l.Account
	if _, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-create", record, "GeneratedUID", l.GUID); err != nil {
		return nil, err
	}
	for _, attribute := range [][2]string{{"UniqueID", strconv.Itoa(l.UID)}, {"PrimaryGroupID", strconv.Itoa(l.GID)}, {"NFSHomeDirectory", l.Home}, {"UserShell", "/bin/sh"}, {"RealName", "Sandbox host"}, {"IsHidden", "1"}, {"AuthenticationAuthority", ";DisabledTags;SecureToken"}} {
		if _, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-create", record, attribute[0], attribute[1]); err != nil {
			return nil, err
		}
	}
	if err := macRandomPassword(ctx, l.Account); err != nil {
		return nil, err
	}
	account, err := e.checkMacAccount(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.applied("account"); err != nil {
		return nil, err
	}
	if err := e.ensureMacHome(); err != nil {
		return nil, err
	}
	return account, nil
}

func (e *macExecutor) checkMacAccount(ctx context.Context) (*macAccount, error) {
	l := e.ledger
	account, err := macReadAccount(ctx, l.Account)
	if err != nil {
		return nil, err
	}
	if account.GUID != l.GUID || account.UID != l.UID || account.GID != l.GID || account.Home != l.Home {
		return nil, macFailure("join_mac_account", "the dedicated account's original native identity changed")
	}
	text, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", "/Users/"+l.Account, "IsHidden", "AuthenticationAuthority", "UserShell")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(text), "IsHidden: 1") || !strings.Contains(string(text), ";DisabledTags;SecureToken") || !strings.Contains(string(text), "UserShell: /bin/sh") {
		return nil, macFailure("join_mac_account", "the dedicated account is not hidden with SecureToken disabled")
	}
	text, err = macCommand(ctx, nil, nil, "/usr/sbin/sysadminctl", "-secureTokenStatus", l.Account)
	if err != nil || !strings.Contains(string(text), "DISABLED") {
		return nil, macFailure("join_mac_token", "the dedicated Mac account has no verified disabled SecureToken state")
	}
	text, err = macCommand(ctx, nil, nil, "/usr/bin/dsmemberutil", "checkmembership", "-U", l.Account, "-G", "admin")
	if err != nil || !strings.Contains(string(text), "is not a member") {
		return nil, macFailure("join_mac_account", "the dedicated account is not a verified standard account")
	}
	return account, nil
}

// dscl's interactive passwd prompt reads the echo-disabled terminal. The new
// random password is never put in argv, a file, a log or the ledger.
func macRandomPassword(ctx context.Context, account string) error {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	password := []byte(base64.RawURLEncoding.EncodeToString(random[:]))
	clear(random[:])
	defer clear(password)
	master, slave, err := pty.Open()
	if err != nil {
		return err
	}
	defer master.Close()
	defer slave.Close()
	attributes, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	attributes.Lflag &^= unix.ECHO | unix.ECHONL
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TIOCSETA, attributes); err != nil {
		return err
	}
	limited, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(limited, "/usr/bin/dscl", ".")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		return err
	}
	slave.Close()
	var output macOutput
	readDone := make(chan struct{})
	go func() { _, _ = io.Copy(&output, master); close(readDone) }()
	input := bytes.NewBufferString("passwd /Users/" + account + "\n")
	input.Write(password)
	input.WriteByte('\n')
	input.Write(password)
	input.WriteString("\nquit\n")
	_, writeErr := master.Write(input.Bytes())
	clear(input.Bytes())
	waitErr := command.Wait()
	master.Close()
	<-readDone
	defer clear(output.data)
	if writeErr != nil || waitErr != nil || output.overflow || bytes.Contains(output.data, []byte("DS Error")) || bytes.Contains(output.data, []byte("eDSAuth")) {
		return errors.Join(macFailure("join_mac_password", "the private random-password operation did not complete"), limited.Err())
	}
	return nil
}

func (e *macExecutor) ensureMacHome() error {
	l := e.ledger
	if _, err := e.intent("home", l.Home); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join("/private/var", brand.Name), filepath.Join("/private/var", brand.Name, "hosts"), l.Home, filepath.Join(l.Home, ".ssh")} {
		if err := macRootDir(path, 0755); err != nil {
			return err
		}
	}
	info, err := os.Lstat(l.Home)
	if err != nil {
		return err
	}
	native := info.Sys().(*syscall.Stat_t)
	observed := macFileState{Exists: true, Device: uint64(native.Dev), Inode: native.Ino, Mode: uint32(info.Mode().Perm())}
	if l.HomeRef.Exists && l.HomeRef != observed {
		return macFailure("join_mac_home", "the original Mac home was replaced")
	}
	l.HomeRef = observed
	return e.applied("home")
}
