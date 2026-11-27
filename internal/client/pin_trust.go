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
	"regexp"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The trust check: whether the user's own settings trust the key that a
// host shows, as OpenSSH decides it, and how the client reads that
// verdict from ssh's own log.

// trustKind is what the user's own settings say of the key that a host
// shows.
type trustKind int

const (
	trustUnknown trustKind = iota // no known hosts entry names the key: a first use
	trusted                       // OpenSSH trusted the key
	trustChanged                  // the user's known hosts hold another key for the host
	trustRevoked                  // the user's settings revoke the key
)

// trust is OpenSSH's verdict on the key that a host shows, with that key's
// type and SHA256 fingerprint.
type trust struct {
	kind trustKind
	typ  string
	fp   string
}

// What ssh logs on the connection that asks whether the user's settings
// trust a key (OpenSSH's sshconnect.c and sshconnect2.c). Each pattern is
// a whole line. A line of the host's own can imitate one: readTrust lets
// such a line only make the verdict stricter.
var (
	serverKeyRE = regexp.MustCompile(`^debug1: Server host (?:key|certificate): (\S+) (SHA256:[A-Za-z0-9+/]{43})(?:$|, )`)
	authRE      = regexp.MustCompile(`^(debug1: SSH2_MSG_SERVICE_ACCEPT received|debug1: Authentications that can continue: |debug1: Authentication succeeded|Authenticated to )`)
	changedRE0  = regexp.MustCompile(`^@ +WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! +@$`)
	revokedRE   = regexp.MustCompile(`^(@ +WARNING: REVOKED HOST KEY DETECTED! +@|Host key \S+ \S+ revoked by file )`)
	notKnownRE  = regexp.MustCompile(`^No [A-Za-z0-9-]+ host key is known for \S+ and you have requested strict checking\.$`)
	failedRE    = regexp.MustCompile(`^Host key verification failed\.$`)
	deniedRE    = regexp.MustCompile(`^(?:\S+: )?Permission denied \(.*\)\.$`)
)

// trustHostKey connects to h once, with no login, and asks OpenSSH whether
// the user's own settings trust the key that the host shows. The
// connection keeps the user's known hosts, KnownHostsCommand, DNS and
// certificate authorities, as ssh -G resolves them, and forces only what
// keeps it safe and readable: strict checking, so that ssh never adds a
// key, no question, no login, no shared connection, no write to a known
// hosts file, and SHA256 in its debug log. A log that it cannot read fails
// closed.
//
// With algs, the host shows its key for those host key algorithms, so a
// key of another type can be checked too.
//
// ssh writes its own log to a file of its own (-E), and the client reads
// the verdict from that file alone. A ProxyJump hop is an ssh of its own,
// which writes to stderr: what it says of its own key, or its debug log,
// never counts as the destination's verdict.
func trustHostKey(ctx context.Context, h Host, algs string) (trust, error) {
	if err := checkJoinedProjectionStepV1(ctx, h); err != nil {
		return trust{}, err
	}
	cfg, err := writeWrapperContext(ctx, h)
	if err != nil {
		return trust{}, err
	}
	dir, err := os.MkdirTemp("", brand.Name+"-trust-")
	if err != nil {
		return trust{}, fmt.Errorf("make a folder for ssh's log: %w", err)
	}
	defer os.RemoveAll(dir)
	logFile := filepath.Join(dir, "ssh.log")
	opts := []string{
		"-o", "StrictHostKeyChecking=yes",
		"-o", "BatchMode=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-o", "NoHostAuthenticationForLocalhost=no",
		"-o", "FingerprintHash=sha256",
		"-o", "LogLevel=DEBUG1",
		"-E", logFile,
	}
	if algs != "" {
		opts = append(opts, "-o", "HostKeyAlgorithms="+algs)
	}
	if ok, err := sshKnowsOption("NoHostAuthenticationForProxyCommand=no"); err != nil {
		return trust{}, err
	} else if ok {
		opts = append(opts, "-o", "NoHostAuthenticationForProxyCommand=no")
	}
	tctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	args := slices.Concat([]string{"-F", cfg}, NoForwarding(), NoPortForwards(), OwnConnection(), opts, noLoginArgs,
		[]string{"-T", "--", h.SSH, "true"})
	cmd := exec.CommandContext(tctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errb
	if err := checkJoinedProjectionStepV1(ctx, h); err != nil {
		return trust{}, err
	}
	runErr := runTrustCmd(cmd)
	if err := checkJoinedProjectionStepV1(context.WithoutCancel(ctx), h); err != nil {
		return trust{}, errors.Join(runErr, err)
	}
	if ctx.Err() != nil {
		return trust{}, fmt.Errorf("read the host key of %s: %w", h.Name, ctx.Err())
	}
	// A verdict counts only from an ssh that ended by itself, in time, with
	// a log that the client could read whole.
	b, readErr := os.ReadFile(logFile)
	var t trust
	var why string
	switch {
	case tctx.Err() != nil:
		why = "ssh took more than " + fetchWait.String()
	case readErr != nil:
		why = "its log cannot be read: " + readErr.Error()
	default:
		if t, why = readTrust(string(b), exitCode(runErr)); why == "" {
			return t, nil
		}
	}
	// Words of the client's own (sshFailure): after the key exchange starts,
	// a host's text reaches the log, and a hop's banner reaches stderr.
	msg := sshFailure(string(b), runErr)
	if errb.Len() > 0 {
		msg += "; a ProxyJump or ProxyCommand on the way wrote more, which the command in the hint shows"
	}
	return trust{}, &UserError{
		Msg:  fmt.Sprintf("cannot reach %s to ask whether your SSH config trusts its host key (%s): %s", h.Name, why, msg),
		Hint: strings.Replace(SSHCommand(h.SSH), "ssh ", "ssh -v ", 1) + " (fix the SSH connection or KnownHostsCommand first)",
		Code: "unreachable",
	}
}

// runTrustCmd runs trustHostKey's ssh command. Tests replace it to exercise
// a slow ssh that still exits cleanly.
var runTrustCmd = (*exec.Cmd).Run

// exitCode is the exit code of an ssh that ended by itself, or -1 for one
// that did not start, or that a signal ended.
func exitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee) && ee.Exited():
		return ee.ExitCode()
	}
	return -1
}

// readTrust reads ssh's own log of the connection of trustHostKey, whole,
// and code, the exit code of that ssh. A host's own text reaches that log,
// such as the key ID of its certificate, which can hold line breaks and
// lines that look like ssh's. Such text comes before ssh's own verdict,
// which it cannot stop, and never after ssh's last line. So:
//   - The most severe verdict anywhere wins: revoked or changed, then not
//     known, then trusted. A line of the host's own can only make the
//     answer stricter.
//   - "Not known" needs ssh's own end of that check as the last line of the
//     log: "Host key verification failed.".
//   - "Trusted" needs the login stage and no other verdict anywhere, and an
//     ssh that ended the way a check that passed ends: it logged in and ran
//     the command (exit code 0), or the host refused the login, which is
//     the last line of the log (exit code 255).
//   - Any other end decides nothing: a failure of ssh's own, such as a
//     KnownHostsCommand that fails, a log that ends early, or two different
//     keys. The client then fails closed.
//
// why says what it lacks, and is "" when it decides.
func readTrust(log string, code int) (t trust, why string) {
	var auth, revoked, changed, unknown, failed bool
	last := ""
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			last = l
		}
		if m := serverKeyRE.FindStringSubmatch(l); m != nil {
			if t.fp != "" && m[2] != t.fp {
				return trust{}, "ssh named two different host keys"
			}
			t.typ, t.fp = m[1], m[2]
			continue
		}
		revoked = revoked || revokedRE.MatchString(l)
		changed = changed || changedRE0.MatchString(l)
		unknown = unknown || notKnownRE.MatchString(l)
		failed = failed || failedRE.MatchString(l)
		auth = auth || authRE.MatchString(l)
	}
	clean := auth && !failed && !unknown
	switch {
	case t.fp == "":
		return trust{}, "ssh saw no host key"
	case revoked:
		t.kind = trustRevoked
	case changed:
		t.kind = trustChanged
	case unknown && code == 255 && failedRE.MatchString(last):
		t.kind = trustUnknown
	case clean && code == 0:
		t.kind = trusted // it logged in, and ran the command
	case clean && code == 255 && deniedRE.MatchString(last):
		t.kind = trusted // the host refused the login, after the check
	default:
		return t, "ssh said neither yes nor no"
	}
	return t, ""
}

// sshFailures say what made ssh fail, from the lines of ssh's own that name
// it. The client says it in words of its own and never quotes ssh's lines:
// after the connection starts, a host's text reaches them, such as the key
// ID of its certificate, a ProxyJump hop's banner, or a message of its
// own, which could hold a false Try line. A host that writes such a line
// only changes which of these words the client says.
var sshFailures = []struct {
	re   *regexp.Regexp
	says string
}{
	{regexp.MustCompile(`(?m)^ssh: Could not resolve hostname `), "the host's name does not resolve"},
	{regexp.MustCompile(`(?m)^ssh: connect to host .*: Connection refused\r?$`), "the host refused the connection (Connection refused)"},
	{regexp.MustCompile(`(?m)^ssh: connect to host .*: (?:Connection|Operation) timed out\r?$`), "the connection timed out"},
	{regexp.MustCompile(`(?m)^ssh: connect to host .*: (?:No route to host|Network is unreachable|Host is down)\r?$`), "the network has no way to the host"},
	{regexp.MustCompile(`(?m)^Host key verification failed\.\r?$`), "ssh refused a host key (Host key verification failed)"},
	{regexp.MustCompile(`(?m)^(?:\S+: )?Permission denied \(`), "the host refused the login (Permission denied)"},
	{regexp.MustCompile(`(?m)^(?:kex_exchange_identification: |ssh_exchange_identification: |Connection closed by |Connection reset by )`), "the connection closed before the key exchange"},
}

// sshFailure says, in fixed words, what made ssh fail, from its log or
// stderr, and its exit status.
func sshFailure(text string, runErr error) string {
	for _, f := range sshFailures {
		if f.re.MatchString(text) {
			return f.says
		}
	}
	if code := exitCode(runErr); code > 0 {
		return fmt.Sprintf("ssh failed with exit code %d", code)
	}
	return "ssh failed"
}

// sshFailReason prefers the destination ssh's own -E log over shared stderr,
// so a ProxyJump hop's banner cannot replace the destination's failure reason.
func sshFailReason(sshLog, stderr string, runErr error) string {
	for _, f := range sshFailures {
		if f.re.MatchString(sshLog) {
			return f.says
		}
	}
	return sshFailure(stderr, runErr)
}
