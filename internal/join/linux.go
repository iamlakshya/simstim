//go:build linux

package join

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
)

type linuxExecutor struct {
	dir, helper, account string
	ledger               *ledger
	out                  io.Writer
}

func joinBase() string              { return filepath.Join("/var/lib", brand.Name, "join") }
func joinDir(account string) string { return filepath.Join(joinBase(), account) }
func command(ctx context.Context, input io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}
	cmd.Stdin = input
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	b := output.Bytes()
	if ctx.Err() != nil {
		return b, errors.Join(ctx.Err(), err)
	}
	if output.exceeded {
		return b, codeFailure("join_command", "a preparation command produced too much output")
	}
	if err != nil {
		return b, fmt.Errorf("run %s: %w", filepath.Base(name), err)
	}
	return b, nil
}

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

func openLinuxExecutor(account string, out io.Writer, create bool) (*linuxExecutor, func(), error) {
	if os.Geteuid() != 0 {
		return nil, nil, &code.Error{Code: "join_root", Message: "machine enrolment needs sudo", Hint: "Run the command again and allow its displayed root changes."}
	}
	if !code.ValidAccount(account) {
		return nil, nil, codeFailure("join_account", "the account is invalid")
	}
	if out == nil {
		out = io.Discard
	}
	if create {
		for _, p := range []string{filepath.Join("/var/lib", brand.Name), joinBase(), joinDir(account)} {
			mode := os.FileMode(0o755)
			if p == joinDir(account) {
				mode = 0o700
			}
			if err := protectedDir(p, mode); err != nil {
				return nil, nil, err
			}
		}
	} else if err := protectedPath(joinDir(account), true); err != nil {
		return nil, nil, err
	}
	lock := filepath.Join(joinBase(), ".lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err = protectedPath(lock, false); err != nil {
		f.Close()
		return nil, nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, nil, codeFailure("join_busy", "another join or leave is running")
	}
	unlock := func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
	return &linuxExecutor{dir: joinDir(account), helper: filepath.Join(joinBase(), "bin", "client"), account: account, out: out}, unlock, nil
}

// ApplyLinux executes only the original checked local plan, as root. Each
// mutation has a durable ahead ledger entry; failures retain the leave script.
func ApplyLinux(ctx context.Context, p *LinuxPlan, out io.Writer) (code.Reply, error) {
	var reply code.Reply
	if p == nil {
		return reply, codeFailure("join_plan", "the original join plan is missing")
	}
	if _, err := NewLinuxPlan(p.invite, p.account, p.addresses, p.port, time.Now()); err != nil {
		return reply, err
	}
	if err := ctx.Err(); err != nil {
		return reply, err
	}
	conf, err := os.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return reply, err
	}
	if !HasSSHDInclude(string(conf)) {
		return reply, missingSSHDInclude()
	}
	keys, err := hostFingerprints(ctx)
	if err != nil {
		return reply, err
	}
	prospective := code.Reply{Nonce: p.invite.Nonce, Account: p.account, Addresses: p.addresses, Port: p.port, Fingerprints: keys}
	if _, err = code.EncodeReply(prospective); err != nil {
		return reply, err
	}
	e, unlock, err := openLinuxExecutor(p.account, out, true)
	if err != nil {
		return reply, err
	}
	defer unlock()
	l, err := readLedger(filepath.Join(e.dir, "ledger.json"))
	if errors.Is(err, os.ErrNotExist) {
		l = &ledger{Version: 1, Account: p.account, Nonce: p.invite.Nonce, KeyHash: digest([]byte(p.invite.PublicKey))}
		err = writeLedger(e.dir, l)
	}
	if err != nil {
		return reply, err
	}
	if l.Nonce != p.invite.Nonce || l.KeyHash != digest([]byte(p.invite.PublicKey)) {
		return reply, codeFailure("join_conflict", "this account has another join's ledger")
	}
	e.ledger = l
	if err = e.installLeaveHelper(); err != nil {
		return reply, err
	}
	account, err := e.ensureAccount(ctx)
	if err != nil {
		return reply, err
	}
	// Publish no joining key until the narrow effective SSH policy passed
	// its test and reload. Reverse leave removes that key before relaxing policy.
	if err = e.configureSSHD(ctx); err != nil {
		return reply, err
	}
	if err = e.authoriseKey(ctx, account, p.invite.PublicKey); err != nil {
		return reply, err
	}
	// Linger comes before the account probe. Its actual native root-step
	// journal records whether this invocation created anything.
	if err = e.ensureLinger(ctx, account); err != nil {
		return reply, err
	}
	if err = e.prepareSandbox(ctx, account); err != nil {
		return reply, err
	}
	reply = code.Reply{Nonce: p.invite.Nonce, Account: p.account, Addresses: append([]string(nil), p.addresses...), Port: p.port, Fingerprints: keys}
	if err = reply.Validate(); err != nil {
		return code.Reply{}, err
	}
	if err = ctx.Err(); err != nil {
		return code.Reply{}, err
	}
	e.ledger.Complete = true
	if err = writeLedger(e.dir, e.ledger); err != nil {
		return code.Reply{}, err
	}
	return reply, nil
}
func (e *linuxExecutor) installLeaveHelper() error {
	bin := filepath.Join(joinBase(), "bin")
	if err := protectedDir(bin, 0o755); err != nil {
		return err
	}
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(f, 256<<20))
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if len(body) == 256<<20 {
		return codeFailure("join_helper", "the running CLI is too large to retain for leave")
	}
	e.helper = filepath.Join(bin, "client-"+digest(body))
	if err = protectedPath(e.helper, false); errors.Is(err, os.ErrNotExist) {
		if err = durableFile(e.helper, body, 0o755, 0, 0); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		existing, err := os.ReadFile(e.helper)
		if err != nil {
			return err
		}
		if digest(existing) != digest(body) {
			return codeFailure("join_helper", "the original retained CLI changed")
		}
	}

	script := "#!/bin/sh\nset -eu\nexec " + shellQuote(e.helper) + " host leave --account " + shellQuote(e.account) + " --yes\n"
	return durableFile(filepath.Join(e.dir, "leave.sh"), []byte(script), 0o700, 0, 0)
}
func shellQuote(s string) string     { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (e *linuxExecutor) save() error { return writeLedger(e.dir, e.ledger) }
func (e *linuxExecutor) ahead(entry ledgerEntry) (*ledgerEntry, error) {
	if old := e.ledger.entry(entry.ID); old != nil {
		if old.State == "undone" {
			old.State = "prepared"
			if err := e.save(); err != nil {
				return nil, err
			}
		}
		return old, nil
	}
	entry.State = "prepared"
	e.ledger.Entries = append(e.ledger.Entries, entry)
	if err := e.save(); err != nil {
		return nil, err
	}
	return &e.ledger.Entries[len(e.ledger.Entries)-1], nil
}
func (e *linuxExecutor) applied(entry *ledgerEntry) error { entry.State = "applied"; return e.save() }
func (e *linuxExecutor) ensureAccount(ctx context.Context) (*user.User, error) {
	if entry := e.ledger.entry("account"); entry != nil {
		u, err := user.Lookup(e.account)
		if err == nil {
			u, err = e.verifyCreatedAccount(entry, true)
			if err != nil {
				return nil, err
			}
			if entry.State != "applied" {
				if err = e.applied(entry); err != nil {
					return nil, err
				}
			}
			return u, nil
		}
		if _, ok := err.(user.UnknownUserError); !ok {
			return nil, err
		}
	} else if u, err := user.Lookup(e.account); err == nil {
		if u.Uid == "0" || !cleanAbsolute(u.HomeDir) || u.HomeDir == "/" {
			return nil, codeFailure("join_account", "the existing account has an unsafe uid or home")
		}
		return u, nil
	} else if _, ok := err.(user.UnknownUserError); !ok {
		return nil, err
	}
	uid, err := unusedUID()
	if err != nil {
		return nil, err
	}
	home := filepath.Join("/home", e.account)
	if _, err = os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		return nil, codeFailure("join_account", "the new account's home already exists or cannot be checked")
	}
	gid, err := e.ensurePrivateGroup(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := e.ahead(ledgerEntry{ID: "account", Kind: "account", UID: uid, GID: gid, Home: home, Origin: randomName()})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if _, err = command(ctx, nil, "/usr/sbin/useradd", "--create-home", "--home-dir", entry.Home, "--uid", strconv.Itoa(entry.UID), "--gid", strconv.Itoa(entry.GID), "--no-user-group", "--comment", accountOrigin(entry), "--shell", "/bin/bash", "--password", "!", e.account); err != nil {
		return nil, err
	}
	u, err := e.verifyCreatedAccount(entry, true)
	if err != nil {
		return nil, err
	}
	return u, e.applied(entry)
}
func unusedUID() (int, error) {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 {
			n, _ := strconv.Atoi(f[2])
			used[n] = true
		}
	}
	for n := 1000; n < 60000; n++ {
		if !used[n] {
			return n, nil
		}
	}
	return 0, codeFailure("join_account", "there is no free regular account uid")
}

func hostFingerprints(ctx context.Context) ([]string, error) {
	b, err := command(ctx, nil, "/usr/sbin/sshd", "-T")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "hostkey" {
			continue
		}
		if !cleanAbsolute(f[1]) {
			return nil, codeFailure("join_sshd", "sshd reported an unsafe host-key path")
		}
		pub, err := command(ctx, nil, "/usr/bin/ssh-keygen", "-y", "-P", "", "-f", f[1])
		if err != nil {
			return nil, fmt.Errorf("derive an active SSH public host key: %w", err)
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey(pub)
		if err != nil {
			return nil, codeFailure("join_sshd", "an active SSH host key cannot be read")
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if !slices.Contains(out, fingerprint) {
			out = append(out, fingerprint)
		}
	}
	if len(out) == 0 {
		return nil, codeFailure("join_sshd", "sshd reported no active host keys")
	}
	return out, nil
}

// ProbeCurrentAccount is the fixed local child entry used with runuser. It
// gathers facts as the account, so root's device access cannot hide missing KVM.
func ProbeCurrentAccount(ctx context.Context) (api.CapProbe, error) { return localCapabilityProbe(ctx) }
