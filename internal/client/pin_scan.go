package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/iamlakshya/simstim/internal/brand"
)

type trustStatus int

const (
	trustError trustStatus = iota
	trustTrusted
	trustUnknown
	trustChanged
	trustRevoked
)

// noAuthOpts stops ssh after host-key verification without offering any key,
// password or agent.
var noAuthOpts = []string{
	"-o", "PreferredAuthentications=none",
	"-o", "PubkeyAuthentication=no",
	"-o", "PasswordAuthentication=no",
	"-o", "KbdInteractiveAuthentication=no",
	"-o", "GSSAPIAuthentication=no",
	"-o", "IdentityAgent=none",
}

// pinProbeArgs builds the common options for a one-off ssh connection that
// checks or fetches host keys without reusing a shared ControlMaster.
func pinProbeArgs(cfg string, noAuth, batch bool, extra ...string) []string {
	args := slices.Concat(
		[]string{"-F", cfg},
		NoForwarding(),
		NoPortForwards(),
		OwnConnection(),
	)
	if noAuth {
		args = append(args, noAuthOpts...)
	}
	if batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args,
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-o", "NoHostAuthenticationForLocalhost=no",
		"-o", "FingerprintHash=sha256",
		"-o", "LogLevel=ERROR",
	)
	if sshKnowsNoHostAuthProxy() {
		args = append(args, "-o", "NoHostAuthenticationForProxyCommand=no")
	}
	return append(args, extra...)
}

// tempKnownHostsArgs are the options that isolate ssh to a temporary
// known_hosts file under h's per-host alias.
func tempKnownHostsArgs(h Host, tmpKH, strict string) []string {
	opts := []string{
		"-o", "HostKeyAlias=" + HostKeyAlias(h.Name),
		"-o", "UserKnownHostsFile=" + quoteSSHPath(tmpKH),
		"-o", "GlobalKnownHostsFile=none",
		"-o", "StrictHostKeyChecking=" + strict,
		"-o", "VerifyHostKeyDNS=no",
	}
	if sshKnowsKnownHostsCommand() {
		opts = append(opts, "-o", "KnownHostsCommand=none")
	}
	return opts
}

// checkUserTrust runs one ssh connection with the user's own known_hosts
// settings and StrictHostKeyChecking=yes. When trusted and login works, it
// reads the host's public keys over that verified session.
func checkUserTrust(ctx context.Context, h Host) ([]ssh.PublicKey, trustStatus, error) {
	cfg, err := writeWrapper(h)
	if err != nil {
		return nil, trustError, err
	}
	args := pinProbeArgs(cfg, false, true, "-o", "StrictHostKeyChecking=yes", "-T", "--", h.SSH, "cat /etc/ssh/ssh_host_*_key.pub 2>/dev/null; true")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, trustError, ctx.Err()
	}
	if runErr == nil {
		return parseAuthorizedKeys(out.Bytes()), trustTrusted, nil
	}
	stderr := errb.String()
	var ee *exec.ExitError
	if asExit(runErr, &ee) && ee.ExitCode() == 255 {
		if st := classifyHostKeyStderr(stderr); st != trustError {
			return nil, st, nil
		}
		if strings.Contains(stderr, "Permission denied") {
			// OpenSSH only reaches authentication after host-key verification succeeds.
			return nil, trustTrusted, nil
		}
	}
	return nil, trustError, sshError(h, runErr, stderr)
}

// checkRevoked checks whether the user's own OpenSSH settings mark the host's
// key (or the key of alg) as revoked, without logging in.
func checkRevoked(ctx context.Context, h Host, alg string) error {
	cfg, err := writeWrapper(h)
	if err != nil {
		return err
	}
	extra := []string{"-o", "StrictHostKeyChecking=yes"}
	if alg != "" {
		extra = append(extra, "-o", "HostKeyAlgorithms="+alg)
	}
	extra = append(extra, "-T", "--", h.SSH, "true")
	cmd := exec.CommandContext(ctx, "ssh", pinProbeArgs(cfg, true, true, extra...)...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errb
	_ = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if classifyHostKeyStderr(errb.String()) == trustRevoked {
		return revokedError(h)
	}
	return nil
}

func revokedError(h Host) error {
	return &UserError{
		Msg:  fmt.Sprintf("the host key of %s is marked as revoked in your SSH settings", h.Name),
		Hint: "Do not connect until the host has a new key, and check @revoked or RevokedHostKeys in your SSH settings.",
		Code: "host_key_revoked",
		Kind: KindRefused,
	}
}

func classifyHostKeyStderr(stderr string) trustStatus {
	switch {
	case strings.Contains(stderr, "REVOKED HOST KEY") || strings.Contains(stderr, "marked as revoked") || strings.Contains(stderr, "was revoked"):
		return trustRevoked
	case strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(stderr, "has changed and you have requested strict checking"):
		return trustChanged
	case strings.Contains(stderr, "Host key verification failed"):
		return trustUnknown
	default:
		return trustError
	}
}

// askHostKey lets OpenSSH ask the user to verify the host key fingerprint at
// the terminal, writing the approved key into a temporary file without login.
func askHostKey(ctx context.Context, h Host) ([]ssh.PublicKey, error) {
	cfg, err := writeWrapper(h)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", brand.Name+"-pin-")
	if err != nil {
		return nil, fmt.Errorf("make a temporary folder for the host key: %w", err)
	}
	defer os.RemoveAll(dir)
	tmpKH := filepath.Join(dir, "known_hosts")
	extra := append(tempKnownHostsArgs(h, tmpKH, "ask"), "-T", "--", h.SSH, "true")
	cmd := exec.CommandContext(ctx, "ssh", pinProbeArgs(cfg, true, false, extra...)...)
	cmd.WaitDelay = sshWaitDelay
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.Discard
	var errb bytes.Buffer
	cmd.Stderr = &errb
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	b, _ := os.ReadFile(tmpKH)
	if keys := parseKnownHostKeys(b); len(keys) > 0 {
		return keys, nil
	}
	if strings.Contains(errb.String(), "Host key verification failed") {
		return nil, &UserError{
			Msg:  fmt.Sprintf("host key of %s was not accepted", h.Name),
			Hint: fmt.Sprintf("%s host add %s", brand.Name, h.Name),
			Code: "cancelled",
			Kind: KindCancelled,
		}
	}
	if runErr != nil {
		return nil, sshError(h, runErr, errb.String())
	}
	return nil, &UserError{
		Msg:  "could not read the host key of " + h.Name,
		Hint: SSHCommand(h.SSH) + " (check that this works first)",
		Code: "unreachable",
	}
}

var keyScanAlgs = []string{
	"ssh-ed25519",
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"rsa-sha2-512,rsa-sha2-256,ssh-rsa",
}

func hostKeyAlgFor(k ssh.PublicKey) string {
	if k.Type() == ssh.KeyAlgoRSA {
		return "rsa-sha2-512,rsa-sha2-256,ssh-rsa"
	}
	return k.Type()
}

// fetchHostKeys retrieves public host keys from h without logging in.
// For direct hosts it uses ssh-keyscan; for ProxyJump/ProxyCommand hosts (or
// as a fallback) it runs ssh with StrictHostKeyChecking=accept-new into a
// temporary file and PreferredAuthentications=none.
func fetchHostKeys(ctx context.Context, h Host, wantFP string) ([]ssh.PublicKey, error) {
	if keys := keyscanHostKeys(ctx, h); len(keys) > 0 {
		if wantFP == "" || hasFingerprint(keys, wantFP) {
			return keys, nil
		}
	}
	keys, lastErr := scanHostKeyViaSSH(ctx, h, "")
	if wantFP == "" {
		if len(keys) > 0 {
			return keys, nil
		}
		return nil, lastErr
	}
	if hasFingerprint(keys, wantFP) {
		return keys, nil
	}
	all := append([]ssh.PublicKey(nil), keys...)
	for _, alg := range keyScanAlgs {
		more, err := scanHostKeyViaSSH(ctx, h, alg)
		if err == nil {
			all = sortKeys(append(all, more...))
			if hasFingerprint(all, wantFP) {
				return all, nil
			}
		} else if lastErr == nil {
			lastErr = err
		}
	}
	if len(all) > 0 {
		return all, nil
	}
	return nil, lastErr
}

// keyscanHostKeys runs ssh-keyscan for a direct SSH host (no ProxyJump or
// ProxyCommand). It returns nil if the host uses a proxy or keyscan fails.
func keyscanHostKeys(ctx context.Context, h Host) []ssh.PublicKey {
	if _, err := exec.LookPath("ssh-keyscan"); err != nil {
		return nil
	}
	vals, err := (&SSH{Host: h}).ConfigValues(ctx)
	if err != nil {
		return nil
	}
	if hasProxy(vals["proxyjump"]) || hasProxy(vals["proxycommand"]) {
		return nil
	}
	host := firstVal(vals["hostname"])
	if host == "" || strings.HasPrefix(host, "-") {
		return nil
	}
	args := []string{"-T", "10"}
	if port := firstVal(vals["port"]); port != "" && port != "22" {
		args = append(args, "-p", port)
	}
	args = append(args, host)
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(sctx, "ssh-keyscan", args...)
	cmd.WaitDelay = sshWaitDelay
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	return parseKnownHostKeys(out)
}

func hasProxy(vals []string) bool {
	v := strings.ToLower(firstVal(vals))
	return v != "" && v != "none"
}

func firstVal(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
}

func scanHostKeyViaSSH(ctx context.Context, h Host, alg string) ([]ssh.PublicKey, error) {
	cfg, err := writeWrapper(h)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", brand.Name+"-scan-")
	if err != nil {
		return nil, fmt.Errorf("make a temporary folder for the host key: %w", err)
	}
	defer os.RemoveAll(dir)
	tmpKH := filepath.Join(dir, "known_hosts")
	extra := tempKnownHostsArgs(h, tmpKH, "accept-new")
	if alg != "" {
		extra = append(extra, "-o", "HostKeyAlgorithms="+alg)
	}
	extra = append(extra, "-T", "--", h.SSH, "true")
	cmd := exec.CommandContext(ctx, "ssh", pinProbeArgs(cfg, true, true, extra...)...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errb
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	b, _ := os.ReadFile(tmpKH)
	if keys := parseKnownHostKeys(b); len(keys) > 0 {
		return keys, nil
	}
	if runErr != nil {
		return nil, sshError(h, runErr, errb.String())
	}
	return nil, &UserError{
		Msg:  "could not fetch a host key from " + h.Name,
		Hint: SSHCommand(h.SSH) + " (check that this works first)",
		Code: "unreachable",
	}
}

func writePinFile(ctx context.Context, name string, keys []ssh.PublicKey) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the pinned host key", err)
	}
	p, err := PinPath(name)
	if err != nil {
		return err
	}
	keys = sortKeys(keys)
	if len(keys) == 0 {
		return fmt.Errorf("no host keys to pin for %s", name)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	unlock, err := lockPathContext(ctx, p+".lock", 10*time.Second, true)
	if err != nil {
		return err
	}
	defer unlock()
	alias := HostKeyAlias(name)
	var b strings.Builder
	fmt.Fprintf(&b, "# Pinned host key for %s. Managed by %s.\n", name, brand.Name)
	for _, k := range keys {
		b.WriteString(knownhosts.Line([]string{alias}, k))
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(dir, ".known_hosts-*")
	if err != nil {
		return fmt.Errorf("write the pinned host key: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write the pinned host key: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write the pinned host key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write the pinned host key: %w", err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save the pinned host key: %w", err)
	}
	return nil
}

func parseKnownHostKeys(data []byte) []ssh.PublicKey {
	var out []ssh.PublicKey
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		marker, _, pub, _, _, err := ssh.ParseKnownHosts(line)
		if err != nil || marker != "" || pub == nil {
			continue
		}
		if cert, ok := pub.(*ssh.Certificate); ok {
			pub = cert.Key
		}
		out = append(out, pub)
	}
	return sortKeys(out)
}

func parseAuthorizedKeys(data []byte) []ssh.PublicKey {
	var out []ssh.PublicKey
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(line)
		if err != nil || pub == nil {
			continue
		}
		if cert, ok := pub.(*ssh.Certificate); ok {
			pub = cert.Key
		}
		out = append(out, pub)
	}
	return sortKeys(out)
}

func sortKeys(keys []ssh.PublicKey) []ssh.PublicKey {
	seen := map[string]bool{}
	out := make([]ssh.PublicKey, 0, len(keys))
	for _, k := range keys {
		if k == nil {
			continue
		}
		fp := ssh.FingerprintSHA256(k)
		if !seen[fp] {
			seen[fp] = true
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b ssh.PublicKey) int {
		if ra, rb := keyRank(a.Type()), keyRank(b.Type()); ra != rb {
			return ra - rb
		}
		return strings.Compare(ssh.FingerprintSHA256(a), ssh.FingerprintSHA256(b))
	})
	return out
}

func keyRank(typ string) int {
	switch {
	case typ == ssh.KeyAlgoED25519:
		return 0
	case strings.HasPrefix(typ, "ecdsa-"):
		return 1
	case typ == ssh.KeyAlgoRSA:
		return 2
	default:
		return 3
	}
}

func fingerprintsOf(keys []ssh.PublicKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = ssh.FingerprintSHA256(k)
	}
	return out
}

func hasFingerprint(keys []ssh.PublicKey, want string) bool {
	return slices.Contains(fingerprintsOf(keys), want)
}
