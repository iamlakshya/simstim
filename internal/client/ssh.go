package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// SSH runs the system OpenSSH client for one host. It never edits ~/.ssh/config:
// it passes a generated config that includes the user's own.
type SSH struct {
	Host Host
	// origin retains the original saved provider entry on this computer.
	origin           *Host
	nativeSource     SSHNativeSourceOpener
	ownConnection    bool
	bindingCollector *agentBindingCollector
	// Interactive allows prompts (passwords, host keys, FIDO touches).
	Interactive bool
}

func home() string { h, _ := os.UserHomeDir(); return h }

// wrapperPath is the generated SSH config for one host. Its name carries
// hostKey, so Box and box never share one.
func wrapperPath(h Host) string {
	if path, ok := loginWrapperPath(h.Name); ok {
		return path
	}
	key := HostFileKey(h.Name)
	if key == "" {
		return ""
	}
	return filepath.Join(ConfigDir(), "ssh", "config-"+key)
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") || filepath.Separator == '\\' && strings.HasPrefix(p, "~\\") {
		return filepath.Join(home(), p[2:])
	}
	return p
}

// sshWaitDelay bounds the wait for ssh's output once ssh has ended or was
// killed. A child of ssh, such as a Match exec or a ProxyCommand, can hold
// stdout or stderr open, and without a bound the client would wait for it.
const sshWaitDelay = 2 * time.Second

// sharedDir is the folder of the shared connections' sockets, or "" when
// the client shares no connections: its OpenSSH cannot share them, or no
// private folder can be made.
func sharedDir() string {
	if !controlMaster {
		return ""
	}
	return controlDir()
}

// noForwarding is what a host must never get from this computer: its SSH
// agent, its X11 display and its Kerberos credentials. ssh also runs no
// LocalCommand here.
var noForwarding = [][2]string{
	{"ForwardAgent", "no"},
	{"ForwardX11", "no"},
	{"PermitLocalCommand", "no"},
	{"GSSAPIDelegateCredentials", "no"},
}

// NoForwarding returns the options that go on every ssh command line, from
// noForwarding. ssh keeps the first value that it finds for each of these
// options, and it reads the command line before any config file. So they
// win over a ForwardAgent yes in ~/.ssh/config (decision 0034).
func NoForwarding() []string {
	out := make([]string, 0, 2*len(noForwarding))
	for _, o := range noForwarding {
		out = append(out, "-o", o[0]+"="+o[1])
	}
	return out
}

// NoPortForwards returns the option that clears every port forward and the
// tunnel that a config file names. It goes on every ssh command that opens
// a session, but Forward's, as none of them needs a port forward. Forward
// does without it, because it would clear Forward's own -L options too.
func NoPortForwards() []string {
	return []string{"-o", "ClearAllForwardings=yes"}
}

// OwnConnection returns the options for a connection of its own, which
// neither uses nor starts a shared master: not the client's, and not one
// that the user's config names.
func OwnConnection() []string {
	return []string{"-o", "ControlMaster=no", "-o", "ControlPath=none"}
}

// baseArgs are the options of every ssh command for the host, on the
// host's shared connection.
func (s *SSH) baseArgs() ([]string, error) { return s.args(true) }

// args are the options of every ssh command for the host. With shared, the
// command uses the host's shared connection, and starts it when it is not
// running. Without, it has a connection of its own. When the host has a
// pinned key, ssh checks the key against the pin alone (spec 14.7.1). The
// shared connection's socket names the saved host and its pin
// (controlKey): two hosts with different pins, or one host before and
// after a rekey, never share a master, which checked a key only once.
func (s *SSH) args(shared bool) ([]string, error) {
	return s.argsContext(context.Background(), shared)
}

// argsWith is args with the pin snap, read once by the caller, or with no
// pin when snap is nil.
func (s *SSH) argsWith(snap *pinSnap, shared bool) ([]string, error) {
	return s.argsWithContext(context.Background(), snap, shared)
}

func (s *SSH) argsContext(ctx context.Context, shared bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap, ok, err := loadPin(s.Host.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return s.argsWithContext(ctx, nil, shared)
	}
	return s.argsWithContext(ctx, &snap, shared)
}

func (s *SSH) argsWithContext(ctx context.Context, snap *pinSnap, shared bool) (_ []string, returned error) {
	ctx, finishNative, err := s.beginNativeSource(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, finishNative()) }()
	ctx = s.policyContext(ctx)
	if snap != nil && snap.staged {
		shared = false
	} // an unpublished stage never leaves a reusable master
	cfg, err := writeWrapperContext(ctx, s.Host)
	if err != nil {
		return nil, err
	}
	var pin []string
	var keys []hostKey
	if snap != nil {
		if pin, err = pinArgsContext(ctx, s.Host.Name, snap.path); err != nil {
			return nil, err
		}
		keys = snap.keys
	}
	args := slices.Concat([]string{"-F", cfg}, NoForwarding(), pin)
	d := ""
	if shared {
		d = sharedDir()
	}
	epoch := ""
	if d != "" {
		key := controlKey(s.Host.Name, keys)
		epoch = markControl(d, s.Host.Name, controlEnvKey(key, cfg != wrapperEnvPath(s.Host, false)))
	}
	switch {
	case epoch != "":
		path := filepath.Join(strings.ReplaceAll(epoch, "%", "%%"), controlName(""))
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+sshPathArg(path, runtime.GOOS), "-o", "ControlPersist=600")
	default:
		// A connection of its own, also when no private folder can be
		// made: the user's own ControlPath could name a master that an
		// older client started, with the agent forwarded.
		args = append(args, OwnConnection()...)
	}
	if !s.Interactive {
		args = append(args, "-o", "BatchMode=yes")
	}
	return args, nil
}

// RemoteHelper is the helper path for a version, in the remote shell's syntax.
func RemoteHelper(ver string) string {
	return fmt.Sprintf(`"$HOME/.local/share/%s/bin/%s/%s"`, brand.Name, ver, brand.HostBinary)
}

// Command builds an ssh command that runs remote (a shell command line) on
// the host. It carries the bridge (Start) and Run, so it clears the port
// forwards that a config file names.
func (s *SSH) Command(ctx context.Context, remote string, extra ...string) (*exec.Cmd, error) {
	return s.command(ctx, ctx, remote, extra...)
}

func (s *SSH) command(lockCtx, procCtx context.Context, remote string, extra ...string) (*exec.Cmd, error) {
	if err := s.checkNativeSourceScope(lockCtx); err != nil {
		return nil, err
	}
	args, err := s.connArgs(lockCtx, !s.ownConnection)
	if err != nil {
		return nil, err
	}
	if s.bindingCollector != nil {
		args, err = s.bindingCollector.commandArgs(lockCtx, args)
		if err != nil {
			return nil, err
		}
	}
	args = append(args, NoPortForwards()...)
	args = append(args, extra...)
	args = append(args, "-e", "none", "--", s.Host.SSH, remote)
	cmd := exec.CommandContext(procCtx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	return cmd, nil
}

// sshLogFile is a private temporary file for the destination ssh's -E log.
// A ProxyJump hop runs in its own ssh without -E, so its banner and errors
// go to stderr and never reach this file.
type sshLogFile struct {
	dir      string
	path     string
	agent    *agentNativeLog
	agentMu  sync.Mutex
	agentErr error
}

func newSSHLog() (*sshLogFile, error) {
	dir, err := os.MkdirTemp("", brand.Name+"-ssh-")
	if err != nil {
		return nil, fmt.Errorf("make a folder for the ssh log: %w", err)
	}
	return &sshLogFile{dir: dir, path: filepath.Join(dir, "ssh.log")}, nil
}

func (l *sshLogFile) finish() string {
	if l != nil && l.agent != nil {
		summary, err := l.agent.Finish()
		l.agentMu.Lock()
		l.agentErr = err
		l.agentMu.Unlock()
		return summary
	}
	if l == nil {
		return ""
	}
	b, _ := os.ReadFile(l.path)
	_ = os.RemoveAll(l.dir)
	return string(b)
}

// Run runs a remote command and returns its stdout. The error includes stderr
// only when the remote command itself ran and exited with 1..254.
func (s *SSH) Run(ctx context.Context, remote string, stdin io.Reader) (_ []byte, returned error) {
	ctx, finishNative, err := s.beginNativeSource(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, finishNative()) }()
	ctx, release, err := s.beginSSHStart(ctx, "SSH command")
	if err != nil {
		return nil, err
	}
	defer release()
	logFile, err := newSSHLog()
	if err != nil {
		return nil, err
	}
	cmd, err := s.Command(ctx, remote, "-T", "-E", logFile.path)
	if err != nil {
		logFile.finish()
		return nil, err
	}
	var out, errb bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if s.Interactive {
		// The user sees what the host writes, as it comes, but cleaned.
		shown := &cleanWriter{w: os.Stderr}
		defer shown.Flush()
		cmd.Stderr = io.MultiWriter(&errb, shown)
	}
	runErr := cmd.Start()
	release()
	if runErr == nil {
		runErr = cmd.Wait()
	}
	sshLog := logFile.finish()
	if s.Host.SSH != "" && ValidHostName(s.Host.Name) && !pinned(s.Host.Name) {
		cleanupUnpinnedMaster(ctx, s.Host, cmd.Args)
	}
	if runErr != nil {
		return out.Bytes(), sshError(ctx, s.Host, runErr, sshLog, errb.String())
	}
	return out.Bytes(), nil
}

// cleanupUnpinnedMaster retires every indexed socket that an in-flight
// command may have created while another client forgot the host.
func cleanupUnpinnedMaster(ctx context.Context, h Host, args []string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	for i, arg := range args {
		if i == 0 || args[i-1] != "-o" || !strings.HasPrefix(arg, "ControlPath=") {
			continue
		}
		value := strings.TrimPrefix(arg, "ControlPath=")
		if strings.HasPrefix(value, `"`) {
			var err error
			value, err = strconv.Unquote(value)
			if err != nil {
				continue
			}
		}
		if filepath.Base(value) == controlName("") {
			epoch := strings.ReplaceAll(filepath.Dir(value), "%%", "%")
			base, token := filepath.Dir(epoch), filepath.Base(epoch)
			if privateDir(base) && controlKeyRE.MatchString(token) {
				_ = retireControlEpoch(ctx, base, token)
			}
			continue
		}
		name, ok := strings.CutSuffix(filepath.Base(value), "-%C")
		key, keyOK := strings.CutPrefix(name, controlPrefix)
		dir := strings.ReplaceAll(filepath.Dir(value), "%%", "%")
		if ok && keyOK && (controlKeyRE.MatchString(key) || legacyControlKeyRE.MatchString(key)) && privateDir(dir) {
			retireControlKey(ctx, dir, key)
		}
	}
	_ = retireControlsAt(ctx, h.Name)
}

// ExitMaster stops the ControlMaster for this host. Call it before reconnecting
// after a network change, because a master on a dead TCP connection hangs new sessions.
func (s *SSH) ExitMaster(ctx context.Context) {
	if s.Host.SSH == "" || sharedDir() == "" {
		return // no master of ours, and no command line to build or file to write
	}
	args, err := s.baseArgs()
	if err != nil {
		return
	}
	s.exitWith(ctx, args)
}

// exitWith stops the master that args, the options of a command, name.
func (s *SSH) exitWith(ctx context.Context, args []string) {
	if s.Host.SSH == "" || sharedDir() == "" {
		return // no master of ours: never stop one that the user's config names
	}
	args = append(slices.Clone(args), "-O", "exit", "--", s.Host.SSH)
	// A master on a dead connection must not hang the reconnect for long.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = sshWaitDelay
	_ = cmd.Run()
}

// CheckMaster asks whether the master of the host's shared connection
// runs, with `ssh -O check`, and returns its process ID.
func (s *SSH) CheckMaster(ctx context.Context) (running bool, pid int) {
	if sharedDir() == "" {
		return false, 0 // no master of ours
	}
	args, err := s.baseArgs()
	if err != nil {
		return false, 0
	}
	args = append(args, "-O", "check", "--", s.Host.SSH)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := masterCommand(ctx, args).CombinedOutput()
	return parseMaster(string(out))
}

// sshError turns ssh's exit status, destination -E log and stderr into a
// user error. A pin that stopped the connection is a refusal (spec 14.14),
// read from sshLog alone. Timeout, cancellation and signal death never quote
// stderr.
func sshError(ctx context.Context, h Host, err error, sshLog, stderr string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	code := exitCode(err)
	if code == 255 {
		if ke := hostKeyError(h, sshLog, code); ke != nil {
			return ke
		}
		// ssh's own failure, in words of the client's own (sshFailReason): a
		// host's banner or message, or a hop's, can reach stderr.
		return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", h.Name, sshFailReason(sshLog, stderr, err)), Hint: SSHCommand(h.SSH) + " (check that this works first)", Code: "unreachable"}
	}
	if code <= 0 {
		return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", h.Name, sshFailure("", err)), Hint: SSHCommand(h.SSH) + " (check that this works first)", Code: "unreachable"}
	}
	msg := tailStderr(stderr) // the remote command's own words, at exit 1..254
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// Shell builds an interactive ssh command that opens a login shell on the
// host, with no forwarding.
func (s *SSH) Shell(ctx context.Context) (*exec.Cmd, error) {
	if err := s.checkNativeSourceScope(ctx); err != nil {
		return nil, err
	}
	args, err := s.connArgs(ctx, true)
	if err != nil {
		return nil, err
	}
	args = append(args, NoPortForwards()...)
	args = append(args, "-t", "--", s.Host.SSH)
	return exec.CommandContext(ctx, "ssh", args...), nil
}
