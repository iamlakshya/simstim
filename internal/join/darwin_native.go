//go:build darwin

package join

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/macfacts"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/crypto/ssh"
)

type macExecutor struct {
	dir    string
	ledger *macLedger
	out    io.Writer
	plan   *DarwinPlan
}

type macAccount struct {
	Name, GUID, Home string
	UID, GID         int
}

type macOutput struct {
	data     []byte
	overflow bool
}

func (w *macOutput) Write(data []byte) (int, error) {
	if len(data) > (1<<20)-len(w.data) {
		w.overflow = true
		return 0, io.ErrShortWrite
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

// macCommand has no shell. Its callers select fixed system commands or the
// exact pinned Lima program; no ledger/reply chooses executable or arguments.
func macCommand(ctx context.Context, account *macAccount, input io.Reader, program string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "HOME=/var/root"}
	if account != nil {
		command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "HOME=" + account.Home}
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(account.UID), Gid: uint32(account.GID), Groups: []uint32{uint32(account.GID)}}}
	}
	command.Stdin = input
	command.WaitDelay = 2 * time.Second
	var output macOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		return nil, errors.Join(ctx.Err(), err)
	}
	if output.overflow {
		return nil, macFailure("join_mac_output", "a fixed Mac command exceeded its output bound")
	}
	if err != nil {
		return output.data, fmt.Errorf("run Mac %s: %w", filepath.Base(program), err)
	}
	return output.data, nil
}

func macUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 15) | 64
	value[8] = (value[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func macReadAccount(ctx context.Context, name string) (*macAccount, error) {
	if !code.ValidAccount(name) {
		return nil, macFailure("join_mac_account", "the Mac account name is invalid")
	}
	data, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UniqueID", "PrimaryGroupID", "GeneratedUID", "NFSHomeDirectory")
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	uid, e1 := strconv.Atoi(values["UniqueID"])
	gid, e2 := strconv.Atoi(values["PrimaryGroupID"])
	if e1 != nil || e2 != nil || uid <= 0 || gid <= 0 || values["GeneratedUID"] == "" || !filepath.IsAbs(values["NFSHomeDirectory"]) {
		return nil, macFailure("join_mac_account", "the native Mac account fields are unavailable")
	}
	return &macAccount{Name: name, GUID: strings.ToLower(values["GeneratedUID"]), UID: uid, GID: gid, Home: values["NFSHomeDirectory"]}, nil
}

func macRemoteLogin(ctx context.Context) error {
	text, err := macCommand(ctx, nil, nil, "/bin/launchctl", "print-disabled", "system")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(text), "\n") {
		if !strings.Contains(line, `"com.openssh.sshd"`) {
			continue
		}
		if strings.Contains(line, "=> disabled") || strings.Contains(line, "=> true") {
			return &code.Error{Code: "join_remote_login", Message: "Remote Login is off", Hint: "Turn it on in System Settings > General > Sharing > Remote Login, then run host join again."}
		}
		if strings.Contains(line, "=> enabled") || strings.Contains(line, "=> false") {
			return nil
		}
	}
	// No override alone does not prove Remote Login is enabled.
	if _, err := macCommand(ctx, nil, nil, "/bin/launchctl", "print", "system/com.openssh.sshd"); err != nil {
		return macFailure("join_remote_login", "Remote Login could not be verified; enable it in System Settings before joining")
	}
	return nil
}

func macSSHMembership(ctx context.Context, account string) error {
	data, err := macCommand(ctx, nil, nil, "/usr/bin/dscl", ".", "-list", "/Groups")
	if err != nil {
		return err
	}
	if !containsMacLine(string(data), "com.apple.access_ssh") {
		return nil
	}
	data, err = macCommand(ctx, nil, nil, "/usr/bin/dsmemberutil", "checkmembership", "-U", account, "-G", "com.apple.access_ssh")
	if err == nil && strings.Contains(string(data), "is a member of the group") && !strings.Contains(string(data), "is not a member") {
		return nil
	}
	return &code.Error{Code: "join_remote_login", Message: "Remote Login does not allow the dedicated account " + account, Hint: "Add " + account + " to Allow access for in System Settings > General > Sharing > Remote Login, then run host join again."}
}

func containsMacLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func macHardware(ctx context.Context, options DarwinOptions) (DarwinOptions, error) {
	if runtime.GOARCH != "arm64" {
		return options, macFailure("join_mac_platform", "a Mac host needs Apple silicon")
	}
	version, err := macCommand(ctx, nil, nil, "/usr/bin/sw_vers", "-productVersion")
	if err != nil || !macfacts.SupportedMacOS(strings.TrimSpace(string(version))) {
		return options, macFailure("join_mac_platform", "a Mac host needs macOS 15 or later")
	}
	cpuData, err := macCommand(ctx, nil, nil, "/usr/sbin/sysctl", "-n", "hw.ncpu")
	if err != nil {
		return options, err
	}
	memoryData, err := macCommand(ctx, nil, nil, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return options, err
	}
	cpus, e1 := strconv.Atoi(strings.TrimSpace(string(cpuData)))
	memory, e2 := strconv.ParseInt(strings.TrimSpace(string(memoryData)), 10, 64)
	if e1 != nil || e2 != nil || cpus < 4 || memory < 4<<30 {
		return options, macFailure("join_mac_size", "the Mac has no room for a VM below half its CPU and memory")
	}
	if options.CPUs == 0 {
		options.CPUs = min(4, (cpus-1)/2)
	}
	if options.MemoryBytes == 0 {
		options.MemoryBytes = min(4<<30, memory/2-(512<<20))
		options.MemoryBytes = options.MemoryBytes / (1 << 20) * (1 << 20)
	}
	if options.CPUs <= 0 || options.CPUs*2 >= cpus || options.MemoryBytes < 256<<20 || options.MemoryBytes >= memory/2 || options.MemoryBytes%(1<<20) != 0 {
		return options, macFailure("join_mac_size", "the VM must use positive whole MiB and less than half the Mac's CPUs and memory")
	}
	return options, nil
}

func openMacExecutor(account string, out io.Writer, create bool) (*macExecutor, func(), error) {
	if os.Geteuid() != 0 {
		return nil, nil, macFailure("join_root", "the Mac join and leave executor need the original sudo operation")
	}
	if !code.ValidAccount(account) {
		return nil, nil, macFailure("join_account", "the dedicated Mac account is invalid")
	}
	if out == nil {
		out = io.Discard
	}
	baseParent := filepath.Join("/private/var/db", brand.Name)
	if create {
		for _, path := range []string{baseParent, macRootBase(), macJoinDir(account)} {
			if err := macRootDir(path, 0700); err != nil {
				return nil, nil, err
			}
		}
	}
	if _, err := macProtected(macJoinDir(account), true); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(macRootBase(), ".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, nil, err
	}
	if _, err := macProtected(lock.Name(), false); err != nil {
		lock.Close()
		return nil, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, macFailure("join_busy", "another Mac join or leave owns the root ledger lock")
	}
	close := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
	return &macExecutor{dir: macJoinDir(account), out: out}, close, nil
}

func macHostKeys(ctx context.Context) ([]string, error) {
	var result []string
	for _, name := range []string{"ssh_host_ed25519_key.pub", "ssh_host_ecdsa_key.pub", "ssh_host_rsa_key.pub"} {
		data, _, err := macReadFile(filepath.Join("/private/etc/ssh", name), 64<<10)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		key, _, _, _, err := sshParsePublic(data)
		if err != nil {
			return nil, err
		}
		result = append(result, key)
	}
	if len(result) == 0 {
		return nil, macFailure("join_mac_host_key", "the Mac has no readable public sshd host keys")
	}
	return result, nil
}

// ApplyDarwin performs the real root-owned Mac join after local consent. It
// never enables Remote Login, changes battery policy or reboots the machine.
func ApplyDarwin(ctx context.Context, p *DarwinPlan, out io.Writer) (code.Reply, error) {
	var reply code.Reply
	if p == nil {
		return reply, macFailure("join_plan", "the original Mac join plan is missing")
	}
	if _, err := NewDarwinPlan(p.invite, p.account, p.addresses, p.port, p.options, macManifest(p), time.Now()); err != nil {
		return reply, err
	}
	options, err := macHardware(ctx, p.options)
	if err != nil {
		return reply, err
	}
	if err := macRemoteLogin(ctx); err != nil {
		return reply, err
	}
	outerKeys, err := macHostKeys(ctx)
	if err != nil {
		return reply, err
	}
	if err := preflightMacReply(p, outerKeys); err != nil {
		return reply, err
	}
	e, close, err := openMacExecutor(p.account, out, true)
	if err != nil {
		return reply, err
	}
	defer close()
	e.plan = p
	ledger, err := readMacLedger(e.dir)
	if errors.Is(err, os.ErrNotExist) {
		guid, gerr := macUUID()
		if gerr != nil {
			return reply, gerr
		}
		ledger = &macLedger{Version: 1, Account: p.account, Nonce: p.invite.Nonce, KeyHash: macDigest([]byte(p.invite.PublicKey)), GUID: guid, Instance: p.account + "-host", Options: options, LimaHash: p.lima.SHA256, ImageHash: p.image.SHA256}
		err = nil
	}
	if err != nil {
		return reply, err
	}
	if ledger.Account != p.account || ledger.Nonce != p.invite.Nonce || ledger.KeyHash != macDigest([]byte(p.invite.PublicKey)) || ledger.Options != options || ledger.LimaHash != p.lima.SHA256 || ledger.ImageHash != p.image.SHA256 {
		return reply, macFailure("join_conflict", "the dedicated Mac account has another original join ledger")
	}
	e.ledger = ledger
	if err := e.validateMacLedger(); err != nil {
		return reply, err
	}
	for _, step := range ledger.Steps {
		if step.State == "undone" && (step.ID == "account" || step.ID == "home" || step.ID == "runtime" || step.ID == "vm" || step.ID == "helper") {
			return reply, macFailure("join_conflict", "the original Mac leave must finish before this account can join again")
		}
	}
	if err := e.save(); err != nil {
		return reply, err
	}
	if err := e.installOriginalHelper(ctx); err != nil {
		return reply, err
	}
	account, err := e.ensureMacAccount(ctx)
	if err != nil {
		return reply, err
	}
	if err := macSSHMembership(ctx, p.account); err != nil {
		return reply, err
	}
	if err := e.installMacRuntime(ctx); err != nil {
		return reply, err
	}
	if err := e.prepareMacVM(ctx, account); err != nil {
		return reply, err
	}
	if err := e.configureMacSSH(ctx, account); err != nil {
		return reply, err
	}
	if err := e.startMacVM(ctx, account); err != nil {
		return reply, err
	}
	if options.AllowACSleepChange {
		if err := e.changeACSleep(ctx); err != nil {
			return reply, err
		}
	}
	if err := e.publishMacFacts(ctx, account); err != nil {
		return reply, err
	}
	vmOwner, err := e.vmOwner(ctx, account)
	if err != nil {
		return reply, err
	}
	guestKeys, err := e.guestHostKeys(ctx, vmOwner)
	if err != nil {
		return reply, err
	}
	facts := macfacts.Read(ctx)
	tier := "own"
	if !facts.MDMKnown || facts.MDMEnrolled {
		tier = "shared"
	}
	reply = code.Reply{Nonce: p.invite.Nonce, Account: p.account, Addresses: []string{"127.0.0.1"}, Port: e.ledger.GuestPort, Fingerprints: guestKeys, SuggestedTier: tier, Hops: []code.Hop{{Account: p.account, Addresses: append([]string(nil), p.addresses...), Port: p.port, Fingerprints: outerKeys}}}
	if _, err := code.EncodeReply(reply); err != nil {
		return code.Reply{}, err
	}
	e.ledger.Complete = true
	if err := e.save(); err != nil {
		return code.Reply{}, err
	}
	fmt.Fprintln(e.out, "The Mac host is prepared. Keep its lid open, lock its screen and log in locally after any restart.")
	return reply, nil
}

func macManifest(p *DarwinPlan) supply.Manifest {
	return supply.Manifest{Release: p.release, Runtime: p.runtime, Items: []supply.Item{p.lima, p.image}, Advisories: append([]supply.Advisory(nil), p.advisories...)}
}

func sshParsePublic(data []byte) (string, string, []string, []byte, error) {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey(bytes.TrimSpace(data))
	if err != nil {
		return "", "", nil, nil, err
	}
	return ssh.FingerprintSHA256(key), comment, options, rest, nil
}

// ResolveDarwinOptions reads only the actual Mac dimensions before the plan is displayed.
func ResolveDarwinOptions(ctx context.Context, options DarwinOptions) (DarwinOptions, error) {
	return macHardware(ctx, options)
}

func preflightMacReply(p *DarwinPlan, outerKeys []string) error {
	var guestKeys []string
	// Reserve the complete wire limit, even though the pinned guest currently
	// supplies three host-key algorithms. No unknown key becomes a real pin.
	for i := 0; i < 8; i++ {
		var hash [32]byte
		hash[0] = byte(i + 1)
		guestKeys = append(guestKeys, "SHA256:"+base64.RawStdEncoding.EncodeToString(hash[:]))
	}
	candidate := code.Reply{Nonce: p.invite.Nonce, Account: p.account, Addresses: []string{"127.0.0.1"}, Port: 65535, Fingerprints: guestKeys, SuggestedTier: "throwaway", Hops: []code.Hop{{Account: p.account, Addresses: append([]string(nil), p.addresses...), Port: p.port, Fingerprints: outerKeys}}}
	if _, err := code.EncodeReply(candidate); err != nil {
		return &code.Error{Code: "code_check", Message: "the Mac reply address and key set would exceed its bounded code", Hint: "Pass fewer --address values before joining the Mac."}
	}
	return nil
}
