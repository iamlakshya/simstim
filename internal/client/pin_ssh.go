package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// What PinHostKey asks of ssh: which options it knows, the key that a host
// shows on a connection that logs in to nothing, and whether the user's own
// settings trust that key. OpenSSH decides that last question itself.

// sshGWait is how long `ssh -G` may take. Tests shorten it.
var sshGWait = 10 * time.Second

// sshGRuns counts the runs of sshG. A test compares it with the runs of
// `ssh -G` that ssh saw: every reader of `ssh -G` uses sshG, so that each
// gets its wait, its parser and its errors.
var sshGRuns atomic.Int64

// sshG runs `ssh -G` with args before "--" and target after it: the values
// that ssh would use, read from its config, with no connection. It returns
// each key, in lower case, with its values, and ssh's stderr on a failure.
func sshG(ctx context.Context, args []string, target string) (map[string][]string, string, error) {
	sshGRuns.Add(1)
	ctx, cancel := context.WithTimeout(ctx, sshGWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", slices.Concat(args, []string{"-G", "--", target})...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, errb.String(), fmt.Errorf("ssh -G %s: %w", target, err)
	}
	vals, err := parseConfigValues(out)
	return vals, errb.String(), err
}

// hostKeyValues reads ssh's values for h through its generated config, the
// user's own config included.
func hostKeyValues(ctx context.Context, h Host) (map[string][]string, error) {
	cfg, err := writeWrapperContext(ctx, h)
	if err != nil {
		return nil, err
	}
	vals, stderr, err := sshG(ctx, []string{"-F", cfg, "-o", "BatchMode=yes"}, h.SSH)
	if err != nil {
		msg := tailStderr(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, &UserError{Msg: fmt.Sprintf("cannot read the SSH config for %s: %s", h.Name, msg),
			Hint: sshGCommand(h.SSH) + " (check that this works first)", Code: "ssh_failed"}
	}
	return vals, nil
}

// first is the first value of key, or "".
func first(vals map[string][]string, key string) string {
	if v := vals[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// takesNewKeys reports whether ssh, with this StrictHostKeyChecking, takes
// a new host key without a question.
func takesNewKeys(v string) bool {
	switch strings.ToLower(v) {
	case "no", "false", "off", "accept-new":
		return true
	}
	return false
}

var (
	knowsMu sync.Mutex
	knows   = map[string]bool{}
)

// unknownOptionRE is what ssh says of an option that it does not know, or
// that this build leaves out.
var unknownOptionRE = regexp.MustCompile(`(?i)(Bad configuration option|Unsupported option)`)

// sshKnowsOption reports whether this computer's ssh takes the option opt,
// such as "KnownHostsCommand=none". It asks `ssh -G` once for each option
// and run, which connects to nothing. Only ssh's own words say that it
// does not know the option. A timeout or another failure is an error,
// which is not kept, so the next command asks again. Tests replace it.
var sshKnowsOption = knowsOption

func knowsOption(opt string) (bool, error) {
	return knowsOptionContext(context.Background(), opt)
}

// sshKnowsOptionContext carries a command's cancellation through the local
// SSH capability query and the wait for another query to finish.
var sshKnowsOptionContext = knowsOptionContext

func knowsOptionContext(ctx context.Context, opt string) (bool, error) {
	for !knowsMu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	defer knowsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if v, ok := knows[opt]; ok {
		return v, nil
	}
	_, stderr, err := sshG(ctx, []string{"-F", os.DevNull, "-o", opt}, "probe.invalid")
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	switch {
	case unknownOptionRE.MatchString(stderr):
		knows[opt] = false
		return false, nil
	case err == nil:
		knows[opt] = true
		return true, nil
	}
	return false, &UserError{Msg: fmt.Sprintf("cannot tell whether this computer's ssh knows %s: %s", opt, firstLine(stderr, err)),
		Hint: "ssh -G -o " + opt + " probe.invalid (check that this works first)", Code: "ssh_failed"}
}

// firstLine is the first line of stderr, cleaned, or err's text.
func firstLine(stderr string, err error) string {
	if l, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); l != "" {
		return CleanLine(l)
	}
	return err.Error()
}

// optionalPinArgs are the pin's options that only some versions of ssh
// know, for the versions that know them. KnownHostsCommand came in OpenSSH
// 8.5. NoHostAuthenticationForProxyCommand turns off the check of host
// keys for a ProxyCommand in the ssh builds that have it.
func optionalPinArgs() ([]string, error) {
	return optionalPinArgsContext(context.Background())
}

func optionalPinArgsContext(ctx context.Context) ([]string, error) {
	var args []string
	for _, o := range []string{"KnownHostsCommand=none", "NoHostAuthenticationForProxyCommand=no"} {
		ok, err := sshKnowsOptionContext(ctx, o)
		if err != nil {
			return nil, err
		}
		if ok {
			args = append(args, "-o", o)
		}
	}
	return args, nil
}

// noLoginArgs make a connection that offers no key and no password, uses
// no agent, and ends when the host refuses the login "none".
var noLoginArgs = []string{
	"-o", "PreferredAuthentications=none",
	"-o", "PubkeyAuthentication=no",
	"-o", "PasswordAuthentication=no",
	"-o", "KbdInteractiveAuthentication=no",
	"-o", "IdentityAgent=none",
}

// How long the connection that reads a host key may take: without a
// question, and with ssh's question to the user. Tests shorten them.
var (
	fetchWait    = time.Minute
	fetchAskWait = 5 * time.Minute
)

// fetchHostKey connects to h once and returns the keys that it shows, with
// the host key algorithms algs, or ssh's own when algs is "". The
// connection offers no key and no password, uses no agent and no shared
// connection, and ends after the key exchange, when the host refuses the
// login. ssh writes the key to a temporary file, never to the user's known
// hosts. With ask, ssh shows the key's fingerprint and asks the user;
// otherwise it takes the key. What the host sends, such as its banner,
// reaches no terminal: ssh logs only errors, and the client keeps them for
// its own message, cleaned.
func fetchHostKey(ctx context.Context, h Host, ask bool, algs string) ([]hostKey, error) {
	cfg, err := writeWrapperContext(ctx, h)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", brand.Name+"-hostkey-")
	if err != nil {
		return nil, fmt.Errorf("make a folder for the host key: %w", err)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		return nil, fmt.Errorf("make a file for the host key: %w", err)
	}
	opts, err := fetchArgsContext(ctx, h.Name, file, ask)
	if err != nil {
		return nil, err
	}
	if algs != "" {
		opts = append(opts, "-o", "HostKeyAlgorithms="+algs)
	}
	wait := fetchWait
	if ask {
		wait = fetchAskWait
	}
	fctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	args := slices.Concat([]string{"-F", cfg}, NoForwarding(), NoPortForwards(), OwnConnection(), opts, []string{"-T", "--", h.SSH, "true"})
	cmd := exec.CommandContext(fctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errb
	runErr := cmd.Run()
	b, _ := os.ReadFile(file)
	keys := readKeys(b)
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("read the host key of %s: %w", h.Name, ctx.Err())
	case fctx.Err() != nil:
		return nil, &UserError{Msg: fmt.Sprintf("%s did not show its host key within %s", h.Name, wait),
			Hint: SSHCommand(h.SSH) + " (check that this works first)", Code: "unreachable"}
	case len(keys) > 0:
		return keys, nil
	case ask && strings.Contains(errb.String(), "Host key verification failed"):
		return nil, &UserError{Msg: fmt.Sprintf("the host key of %s was not accepted, so nothing was pinned", h.Name),
			Hint: "Check the key on the machine, then run this again, or pass --host-key SHA256:…", Code: "cancelled", Kind: KindCancelled}
	}
	return nil, &UserError{Msg: fmt.Sprintf("cannot reach %s to read its host key: %s", h.Name, sshFailure(errb.String(), runErr)),
		Hint: SSHCommand(h.SSH) + " (check that this works first)", Code: "unreachable"}
}

// fetchArgs are the options of the connection that reads a host key into
// file. Like the pin's options, they keep every other source of keys out.
// No method of login is left but "none", which a host refuses.
func fetchArgs(name, file string, ask bool) ([]string, error) {
	return fetchArgsContext(context.Background(), name, file, ask)
}

func fetchArgsContext(ctx context.Context, name, file string, ask bool) ([]string, error) {
	strict, batch := "accept-new", "yes"
	if ask {
		strict, batch = "ask", "no"
	}
	args := make([]string, 0, 40)
	args = append(args,
		"-o", "HostKeyAlias="+pinAlias(name),
		"-o", "UserKnownHostsFile="+sshPathArg(file, runtime.GOOS),
		"-o", "StrictHostKeyChecking="+strict,
		"-o", "UpdateHostKeys=no",
		"-o", "GlobalKnownHostsFile="+os.DevNull,
		"-o", "VerifyHostKeyDNS=no",
		"-o", "NoHostAuthenticationForLocalhost=no",
		"-o", "CheckHostIP=no",
		"-o", "HashKnownHosts=no",
		"-o", "FingerprintHash=sha256",
		"-o", "BatchMode="+batch,
		"-o", "LogLevel=ERROR",
	)
	args = append(args, noLoginArgs...)
	more, err := optionalPinArgsContext(ctx)
	return append(args, more...), err
}

// lockPin takes the lock of the pin of the host called name, so that two
// clients never pin one host at once. It waits while ctx lasts, and ten
// minutes at most.
func lockPin(ctx context.Context, name string) (unlock func(), err error) {
	return lockPinAs(ctx, name, true)
}

// readPinLocked runs read under the pin's lock, shared: readers wait for a
// client that writes or removes the pin, and do not wait for each other.
var readPinLocked = func(ctx context.Context, name string, read func()) error {
	unlock, err := lockPinAs(ctx, name, false)
	if err != nil {
		return err
	}
	defer unlock()
	read()
	return nil
}

// lockPinAs is lockPin, exclusive or shared.
func lockPinAs(ctx context.Context, name string, exclusive bool) (unlock func(), err error) {
	if err := checkHostPinOrder(ctx); err != nil {
		return nil, err
	}
	path := lockFile(name)
	if path == "" {
		return nil, &UserError{Msg: fmt.Sprintf("%q cannot name a host", CleanLine(name)),
			Hint: "Fix the host's name in " + hostsPath() + ": letters, digits, dots, dashes and underscores, with no dot at the end.", Code: "bad_name", Kind: KindUsage}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("wait for another client that pins the host key of %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("make %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the lock %s: %w", path, err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		ok, err := tryLockFile(f, exclusive)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if ok {
			pinLockTaken(name)
			if err := ctx.Err(); err != nil {
				_ = unlockFile(f)
				_ = f.Close()
				return nil, fmt.Errorf("wait for another client that pins the host key of %s: %w", name, err)
			}
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, errLockBusy)
		}
		pinLockWaiting(name)
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("wait for another client that pins the host key of %s: %w", name, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// lockPinWait is lockPin without a context, for a step that must not stop
// half way, such as the save of a host with its pin.
var lockPinWait = func(name string) (func(), error) { return lockPin(context.Background(), name) }

// Pin lock hooks let tests place cancellation at acquisition and contention.
var (
	pinLockTaken   = func(string) {}
	pinLockWaiting = func(string) {}
)
