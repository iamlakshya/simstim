package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
)

// PinSource says where a host's pinned SSH key came from.
type PinSource int

const (
	// PinFromKnownHosts means the user's own OpenSSH settings trusted the key.
	PinFromKnownHosts PinSource = iota + 1
	// PinFromFlag means the key matched --host-key.
	PinFromFlag
	// PinOnFirstUse means the key was accepted on first use (prompt, --yes or host create).
	PinOnFirstUse
	// PinExisting means the host already had a pinned key.
	PinExisting
)

// String names the pin source for progress messages.
func (s PinSource) String() string {
	switch s {
	case PinFromKnownHosts:
		return "from your known_hosts"
	case PinFromFlag:
		return "matched --host-key"
	case PinOnFirstUse:
		return "on first use"
	case PinExisting:
		return "already pinned"
	default:
		return ""
	}
}

// PinOptions configures PinHostKey.
type PinOptions struct {
	// HostKey is an expected SHA256:... fingerprint from --host-key.
	HostKey string
	// AcceptFirstUse pins an unknown host key without asking (--yes or host create).
	AcceptFirstUse bool
	// Interactive allows ssh to ask the user at the terminal on first use.
	Interactive bool
}

// PinResult reports what PinHostKey pinned.
type PinResult struct {
	Fingerprint  string
	Fingerprints []string
	Source       PinSource
	// Created is true when this call wrote a new pin file.
	Created bool
}

// HostKeyAlias is the per-host alias stored in the host's private known_hosts file.
func HostKeyAlias(name string) string { return brand.Name + "-" + name }

// PinPath returns the path to a host's private known_hosts file.
func PinPath(name string) (string, error) {
	if !ValidHostName(name) || strings.HasSuffix(name, ".") {
		return "", &UserError{
			Msg:  fmt.Sprintf("%q cannot name a host", name),
			Hint: "Use letters, digits, dots, dashes and underscores, and do not end with a dot.",
			Code: "bad_name",
			Kind: KindUsage,
		}
	}
	return filepath.Join(ConfigDir(), "ssh", "known_hosts-"+name), nil
}

// HasPin reports whether a host has a private known_hosts pin file.
func HasPin(name string) bool {
	p, err := PinPath(name)
	if err != nil {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// RemovePin deletes a host's private known_hosts pin file if it exists.
// It runs cleanup even if ctx was cancelled, so a failed host add leaves no pin.
func RemovePin(ctx context.Context, name string) error {
	p, err := PinPath(name)
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil
	}
	unlock, lerr := lockPathContext(context.WithoutCancel(ctx), p+".lock", 10*time.Second, true)
	if lerr == nil {
		defer unlock()
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the pinned host key %s: %w", p, err)
	}
	return nil
}

// PinnedFingerprints reads the SHA256 fingerprints pinned for a host.
func PinnedFingerprints(name string) ([]string, error) {
	p, err := PinPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read the pinned host key %s: %w", p, err)
	}
	keys := parseKnownHostKeys(b)
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid host keys in %s", p)
	}
	return fingerprintsOf(keys), nil
}

// NormalizeHostKey validates a SHA256:... fingerprint and returns its canonical form.
func NormalizeHostKey(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	rest, ok := strings.CutPrefix(s, "SHA256:")
	if !ok || rest == "" {
		return "", badHostKeyError(raw)
	}
	rest = strings.TrimRight(rest, "=")
	b, err := base64.RawStdEncoding.DecodeString(rest)
	if err != nil || len(b) != 32 {
		return "", badHostKeyError(raw)
	}
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(b), nil
}

func badHostKeyError(raw string) error {
	return &UserError{
		Msg:  fmt.Sprintf("%q is not a SHA256 host key fingerprint", raw),
		Hint: "Pass a fingerprint starting with SHA256: from ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub.",
		Code: "bad_host_key",
		Kind: KindUsage,
	}
}

// sshKnowsOption asks the system ssh once whether it recognises an option,
// without connecting anywhere.
func sshKnowsOption(opt string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-G", "-F", os.DevNull, "-o", "BatchMode=yes", "-o", opt, "--", "probe.invalid")
	cmd.WaitDelay = sshWaitDelay
	return cmd.Run() == nil
}

var (
	sshKnowsKnownHostsCommand = sync.OnceValue(func() bool { return sshKnowsOption("KnownHostsCommand=none") })
	sshKnowsNoHostAuthProxy   = sync.OnceValue(func() bool { return sshKnowsOption("NoHostAuthenticationForProxyCommand=no") })
)

// quoteSSHPath quotes a path for OpenSSH's UserKnownHostsFile when it holds spaces.
func quoteSSHPath(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// pinArgs returns the command-line options that enforce a host's pinned key,
// or nil when the host has no pin file yet.
func pinArgs(h Host, verbose bool) ([]string, error) {
	if !HasPin(h.Name) {
		return nil, nil
	}
	p, err := PinPath(h.Name)
	if err != nil {
		return nil, err
	}
	opts := []string{
		"-o", "HostKeyAlias=" + HostKeyAlias(h.Name),
		"-o", "UserKnownHostsFile=" + quoteSSHPath(p),
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "GlobalKnownHostsFile=none",
		"-o", "VerifyHostKeyDNS=no",
		"-o", "NoHostAuthenticationForLocalhost=no",
		"-o", "CheckHostIP=no",
		"-o", "FingerprintHash=sha256",
	}
	if !verbose {
		opts = append(opts, "-o", "LogLevel=ERROR")
	}
	if sshKnowsKnownHostsCommand() {
		opts = append(opts, "-o", "KnownHostsCommand=none")
	}
	if sshKnowsNoHostAuthProxy() {
		opts = append(opts, "-o", "NoHostAuthenticationForProxyCommand=no")
	}
	return opts, nil
}

// PinHostKey pins the SSH host key of h into its private known_hosts file.
// It never replaces an existing pin: only ApplyRekey replaces one.
func PinHostKey(ctx context.Context, h Host, opts PinOptions) (PinResult, error) {
	if _, err := PinPath(h.Name); err != nil {
		return PinResult{}, err
	}
	var wantFP string
	if opts.HostKey != "" {
		var err error
		if wantFP, err = NormalizeHostKey(opts.HostKey); err != nil {
			return PinResult{}, err
		}
	}
	if HasPin(h.Name) {
		fps, err := PinnedFingerprints(h.Name)
		if err != nil {
			return PinResult{}, &UserError{
				Msg:  fmt.Sprintf("cannot read the pinned host key of %s: %v", h.Name, err),
				Hint: fmt.Sprintf("%s host rekey %s", brand.Name, h.Name),
				Code: "host_key_unreadable",
				Kind: KindRefused,
			}
		}
		if wantFP != "" && !slices.Contains(fps, wantFP) {
			return PinResult{}, &UserError{
				Msg:  fmt.Sprintf("%s is already pinned to %s, which does not match --host-key %s", h.Name, fps[0], wantFP),
				Hint: fmt.Sprintf("If you meant to replace the pin: %s host rekey %s --host-key %s", brand.Name, h.Name, wantFP),
				Code: "host_key_mismatch",
				Kind: KindRefused,
			}
		}
		return PinResult{Fingerprint: fps[0], Fingerprints: fps, Source: PinExisting}, nil
	}
	if wantFP != "" {
		keys, err := matchHostKeyFlag(ctx, h, wantFP)
		if err != nil {
			return PinResult{}, err
		}
		return saveNewPin(ctx, h, keys, PinFromFlag)
	}
	return pinFromOpenSSH(ctx, h, opts)
}

// FetchRekeyKeys fetches the current public key(s) from h without touching
// any existing pin. If wantHostKey is set, only the matching key is returned.
func FetchRekeyKeys(ctx context.Context, h Host, wantHostKey string) ([]ssh.PublicKey, []string, error) {
	if _, err := PinPath(h.Name); err != nil {
		return nil, nil, err
	}
	if wantHostKey != "" {
		wantFP, err := NormalizeHostKey(wantHostKey)
		if err != nil {
			return nil, nil, err
		}
		keys, err := matchHostKeyFlag(ctx, h, wantFP)
		if err != nil {
			return nil, nil, err
		}
		return keys, fingerprintsOf(keys), nil
	}
	if err := checkRevoked(ctx, h, ""); err != nil {
		return nil, nil, err
	}
	keys, err := fetchHostKeys(ctx, h, "")
	if err != nil {
		return nil, nil, err
	}
	return keys, fingerprintsOf(keys), nil
}

// ApplyRekey writes keys as the new pinned host keys of h and stops its
// shared SSH connection so the new pin takes effect immediately.
func ApplyRekey(ctx context.Context, h Host, keys []ssh.PublicKey) ([]string, error) {
	if err := writePinFile(ctx, h.Name, keys); err != nil {
		return nil, err
	}
	(&SSH{Host: h}).ExitMaster(ctx)
	return fingerprintsOf(keys), nil
}

func saveNewPin(ctx context.Context, h Host, keys []ssh.PublicKey, src PinSource) (PinResult, error) {
	if err := writePinFile(ctx, h.Name, keys); err != nil {
		return PinResult{}, err
	}
	(&SSH{Host: h}).ExitMaster(ctx)
	fps := fingerprintsOf(keys)
	return PinResult{Fingerprint: fps[0], Fingerprints: fps, Source: src, Created: true}, nil
}

func matchHostKeyFlag(ctx context.Context, h Host, wantFP string) ([]ssh.PublicKey, error) {
	keys, err := fetchHostKeys(ctx, h, wantFP)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if ssh.FingerprintSHA256(k) == wantFP {
			if err := checkRevoked(ctx, h, hostKeyAlgFor(k)); err != nil {
				return nil, err
			}
			return []ssh.PublicKey{k}, nil
		}
	}
	shown := fingerprintsOf(keys)
	return nil, &UserError{
		Msg:  fmt.Sprintf("%s did not show host key %s (it showed %s)", h.Name, wantFP, strings.Join(shown, ", ")),
		Hint: "Check the fingerprint on the host with ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub.",
		Code: "host_key_mismatch",
		Kind: KindRefused,
	}
}

func pinFromOpenSSH(ctx context.Context, h Host, opts PinOptions) (PinResult, error) {
	keys, status, sshErr := checkUserTrust(ctx, h)
	switch status {
	case trustTrusted:
		if len(keys) == 0 {
			var err error
			if keys, err = fetchHostKeys(ctx, h, ""); err != nil {
				return PinResult{}, err
			}
		}
		return saveNewPin(ctx, h, keys, PinFromKnownHosts)
	case trustRevoked:
		return PinResult{}, revokedError(h)
	case trustChanged:
		return PinResult{}, &UserError{
			Msg:  "your SSH known_hosts has a different key for " + h.Name,
			Hint: fmt.Sprintf("Check the key on %s (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub), then pass --host-key SHA256:… if it is right.", h.Name),
			Code: "host_key_changed",
			Kind: KindRefused,
		}
	case trustUnknown:
		if opts.AcceptFirstUse {
			keys, err := fetchHostKeys(ctx, h, "")
			if err != nil {
				return PinResult{}, err
			}
			return saveNewPin(ctx, h, keys, PinOnFirstUse)
		}
		if opts.Interactive && InputAllowed(ctx) {
			keys, err := askHostKey(ctx, h)
			if err != nil {
				return PinResult{}, err
			}
			return saveNewPin(ctx, h, keys, PinOnFirstUse)
		}
		return PinResult{}, &UserError{
			Msg:  fmt.Sprintf("the host key of %s is not in your SSH known_hosts, and input is off", h.Name),
			Hint: fmt.Sprintf("%s host add %s --host-key SHA256:… (or --yes to pin on first use)", brand.Name, h.Name),
			Code: "needs_answer",
			Kind: KindNeedsAnswer,
		}
	default:
		return PinResult{}, sshErr
	}
}

var sha256FingerprintRE = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)

// hostKeyChangedError turns an OpenSSH host-key failure on a pinned host into
// the user error of spec 14.14 (exit code 5).
func hostKeyChangedError(h Host, stderr string) *UserError {
	if !HasPin(h.Name) {
		return nil
	}
	if classifyHostKeyStderr(stderr) == trustError {
		return nil
	}
	pinned := "unknown"
	if fps, err := PinnedFingerprints(h.Name); err == nil && len(fps) > 0 {
		pinned = fps[0]
	}
	shown := ""
	if _, after, ok := strings.Cut(stderr, "sent by the remote host is"); ok {
		shown = sha256FingerprintRE.FindString(after)
	}
	if shown == "" {
		shown = sha256FingerprintRE.FindString(stderr)
	}
	msg := fmt.Sprintf("%s's host key changed: %s was pinned, and the host showed a different key", h.Name, pinned)
	if shown != "" {
		msg = fmt.Sprintf("%s's host key changed: %s was pinned, and the host shows %s", h.Name, pinned, shown)
	}
	return &UserError{
		Msg:  msg,
		Hint: fmt.Sprintf("If you reinstalled %s: %s host rekey %s. Otherwise, do not connect.", h.Name, brand.Name, h.Name),
		Code: "host_key_changed",
		Kind: KindRefused,
	}
}

// ForwardError turns a failed forward ssh command into a user error, keeping
// a changed-host-key refusal (exit code 5).
func ForwardError(h Host, err error, stderr string) error {
	var ee *exec.ExitError
	if asExit(err, &ee) && ee.ExitCode() == 255 {
		if ke := hostKeyChangedError(h, stderr); ke != nil {
			return ke
		}
	}
	return &UserError{
		Msg:  "the forward stopped: " + err.Error(),
		Hint: "Check that the local port is free, then try again.",
		Code: "forward_failed",
	}
}
