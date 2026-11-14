package client

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// pinArgs are the options that make ssh check the key of the host called
// name against the pin at path, and against nothing else (spec 14.7.1).
// They go on the command line, where they win over every config file.
// Without the source options, a wildcard or @cert-authority line in the
// system's known hosts file, an SSHFP record, the user's KnownHostsCommand,
// or a config that turns the check off for a loopback address or a
// ProxyCommand could pass another key. Lima's own SSH config turns it off
// for the loopback address. The last three keep the pin's file and ssh's
// message to the client's: CheckHostIP would add lines to the pin,
// another FingerprintHash would hide the SHA256 fingerprint, and another
// LogLevel would hide the message, or show a host's banner, which could
// imitate it.
func pinArgs(name, path string) ([]string, error) {
	args := make([]string, 0, 24)
	args = append(args,
		"-o", "HostKeyAlias="+pinAlias(name),
		"-o", "UserKnownHostsFile="+sshPathArg(path, runtime.GOOS),
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no",
		"-o", "GlobalKnownHostsFile="+os.DevNull,
		"-o", "VerifyHostKeyDNS=no",
		"-o", "NoHostAuthenticationForLocalhost=no",
		"-o", "CheckHostIP=no",
		"-o", "FingerprintHash=sha256",
		"-o", "LogLevel=ERROR",
	)
	more, err := optionalPinArgs()
	if err != nil {
		return nil, err
	}
	return append(args, more...), nil
}

// sshPathArg is a path as the value of an -o option. ssh splits such a
// value at spaces, and reads a backslash as an escape: so the path is
// quoted when it holds a space, and Windows paths use forward slashes,
// which Windows' OpenSSH reads as well.
func sshPathArg(p, system string) string {
	if system == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// fingerprintRE is a SHA256 fingerprint as ssh-keygen -l prints it.
var fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// checkFingerprint refuses a value of --host-key that is not a SHA256
// fingerprint.
func checkFingerprint(want string) error {
	if want == "" || fingerprintRE.MatchString(want) {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("%q is not a SHA256 fingerprint of a host key", CleanLine(want)),
		Hint: "Run ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the host, and pass the SHA256:… it prints.",
		Code: "bad_value", Kind: KindUsage,
	}
}

// PinHostKey pins h's host key (spec 14.7.1), and returns the pin. h is
// reached over SSH: h.SSH is its target, and h.SSHConfig, when set, its own
// config. OpenSSH decides which key the user's own settings trust:
//
//  1. A connection with no login, on the user's own known hosts settings,
//     asks ssh whether they trust the key that the host shows. When they
//     do, the client reads that key on a second connection and pins it.
//     A key that they revoke is refused.
//  2. With want, a SHA256 fingerprint from --host-key, it pins the key that
//     the host shows with that fingerprint, or fails.
//  3. Otherwise it reads the key that the host shows, and pins it as
//     firstUse says.
//
// Both connections offer no key and no password. PinHostKey replaces any
// pin that h has, then stops h's shared connection of before.
func PinHostKey(ctx context.Context, h Host, want string, firstUse FirstUse) (Pin, error) {
	return pinHostKey(ctx, h, want, firstUse, true)
}

// pinHostKey is PinHostKey. Without replace, a pin that another client
// wrote while this one waited for the lock stays, and it must hold want.
func pinHostKey(ctx context.Context, h Host, want string, firstUse FirstUse, replace bool) (Pin, error) {
	if !ValidHostName(h.Name) {
		return Pin{}, &UserError{Msg: fmt.Sprintf("%q cannot name a host", CleanLine(h.Name)), Hint: "Use letters, digits, dots, dashes and underscores, with no dot at the end.", Code: "bad_name", Kind: KindUsage}
	}
	if err := checkFingerprint(want); err != nil {
		return Pin{}, err
	}
	st, err := stageForContext(ctx, h.Name)
	if err != nil {
		return Pin{}, err
	}
	pinStarted(h.Name)
	if err := migratePinContext(ctx, h.Name); err != nil {
		return Pin{}, err
	}
	unlock, err := lockPin(ctx, h.Name)
	if err != nil {
		return Pin{}, err
	}
	defer unlock()
	if err := sameStage(h.Name, st); err != nil {
		return Pin{}, err
	}
	if !replace && pinned(h.Name) {
		return keptPin(ctx, h.Name, want)
	}
	if !replace && !staging(h.Name) {
		// A saved host's pin: the host may have been forgotten while this
		// client waited for the lock.
		if err := stillSaved(h.Name); err != nil {
			return Pin{}, err
		}
	}
	keys, src, err := findHostKey(ctx, h, want, firstUse)
	if err != nil {
		return Pin{}, err
	}
	if err := ctx.Err(); err != nil {
		return Pin{}, err // the command stopped: nothing is written
	}
	if err := ctx.Err(); err != nil {
		return Pin{}, err
	}
	if err := sameStage(h.Name, st); err != nil {
		return Pin{}, err
	}
	if err := writePin(h.Name, st, keys); err != nil {
		return Pin{}, err
	}
	afterPinCommit()
	if err := retireMaster(context.WithoutCancel(ctx), h); err != nil {
		return Pin{}, err
	}
	DebugLog().Info("pinned the host key", "host", h.Name, "source", string(src), "keys", len(keys))
	return pinOf(keys, src), nil
}

// pinStarted runs when pinHostKey has noted its stage. Tests wait for it.
var pinStarted = func(string) {}

// afterPinCommit schedules cancellation at the committed boundary in tests.
var afterPinCommit = func() {}

// sameStage checks, under the pin's lock, that the stage of the host called
// name is still st, the stage of the operation that pins it: a stage that
// closed, or a newer one, means that the operation was stopped, and that
// its pin goes nowhere.
func sameStage(name string, st *stage) error {
	stagesMu.Lock()
	valid := stages[name] == st && (st == nil || !st.closed)
	stagesMu.Unlock()
	if valid {
		return nil
	}
	return &UserError{Msg: "the add of " + name + " stopped while its host key was read, so nothing was pinned",
		Hint: brand.Name + " host add " + name, Code: "cancelled", Kind: KindCancelled}
}

// stillSaved checks, under the pin's lock, that the host called name is
// saved: host rm may have forgotten it while this client waited for the
// lock, and a pin for a host that is gone is never written.
func stillSaved(name string) error {
	ok, err := savedHost(name)
	if err != nil {
		return err
	}
	if !ok {
		return &UserError{Msg: name + " was forgotten while the client waited to pin its host key, so nothing was pinned",
			Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
	}
	return nil
}

// keptPin is the pin that the host called name has, which must hold want.
func keptPin(ctx context.Context, name, want string) (Pin, error) {
	if _, err := stageForContext(ctx, name); err != nil {
		return Pin{}, err
	}
	var keys []hostKey
	if st, ok := stagedPin(name); ok {
		keys = st.keys
	} else {
		var err error
		if keys, err = readPin(name); isForeign(err) {
			return Pin{}, err
		} else if err != nil {
			return Pin{}, &UserError{Msg: fmt.Sprintf("the pinned host key of %s cannot be read: %v", name, err),
				Hint: brand.Name + " host rekey " + name, Code: "host_key_unreadable", Kind: KindRefused}
		}
	}
	if _, ok := withFingerprint(keys, want); want != "" && !ok {
		return Pin{}, pinnedOther(name, keys, want)
	}
	return pinOf(keys, PinKept), nil
}

// findHostKey finds the keys to pin for h, and where they came from.
func findHostKey(ctx context.Context, h Host, want string, firstUse FirstUse) ([]hostKey, PinSource, error) {
	t, err := trustHostKey(ctx, h, "")
	if err != nil {
		return nil, "", err
	}
	switch t.kind {
	case trustRevoked:
		return nil, "", revokedError(h, t.fp)
	case trusted:
		if want == "" || t.fp == want {
			k, err := fetchTrusted(ctx, h, t)
			if err != nil {
				return nil, "", err
			}
			src := PinFromKnownHosts
			if want != "" {
				src = PinMatchedFlag
			}
			return []hostKey{k}, src, nil
		}
	case trustChanged:
		if want == "" {
			return nil, "", &UserError{
				Msg:  fmt.Sprintf("%s shows the host key %s, and your known_hosts holds another key for it", h.Name, t.fp),
				Hint: fmt.Sprintf("If you reinstalled %s, check its key on the machine (%s), and pass it with --host-key. Otherwise, do not connect.", h.Name, checkCommand(t.typ)),
				Code: "host_key_changed", Kind: KindRefused,
			}
		}
	case trustUnknown:
	}
	if want != "" {
		k, err := fetchWanted(ctx, h, want)
		if err == nil {
			err = notRevoked(ctx, h, t, []hostKey{k})
		}
		if err != nil {
			return nil, "", err
		}
		return []hostKey{k}, PinMatchedFlag, nil
	}
	if firstUse == FirstUseRefuse {
		return nil, "", &UncheckedKeyError{Pin: Pin{Fingerprint: t.fp, Type: t.typ, Keys: 1}}
	}
	keys, err := fetchHostKey(ctx, h, firstUse == FirstUseAsk, "")
	if err == nil {
		err = notRevoked(ctx, h, t, keys)
	}
	if err != nil {
		return nil, "", err
	}
	return keys, PinFirstUse, nil
}

// revokedError refuses a key that the user's SSH config revokes.
func revokedError(h Host, fp string) error {
	return &UserError{
		Msg:  fmt.Sprintf("%s shows the host key %s, which your SSH config revokes", h.Name, fp),
		Hint: "Do not connect. Ask the machine's owner for its current key.",
		Code: "host_key_revoked", Kind: KindRefused,
	}
}

// notRevoked asks OpenSSH, for each key to pin that the first verdict t did
// not cover, whether the user's settings revoke it: on a connection of that
// key's own type, whose verdict must name that very key. A host can show a
// trusted key of one type, and a revoked key of another.
func notRevoked(ctx context.Context, h Host, t trust, keys []hostKey) error {
	for _, k := range keys {
		if k.Fingerprint() == t.fp {
			continue
		}
		v, err := trustHostKey(ctx, h, algorithmsFor(k.Type))
		if err != nil {
			return err
		}
		if v.fp != k.Fingerprint() {
			return &UserError{
				Msg:  fmt.Sprintf("%s showed the host key %s, then %s on a connection of the same type", h.Name, k.Fingerprint(), v.fp),
				Hint: "Do not connect. Check the key on the machine: " + checkCommand(k.Type),
				Code: "host_key_mismatch", Kind: KindRefused,
			}
		}
		if v.kind == trustRevoked {
			return revokedError(h, v.fp)
		}
	}
	return nil
}

// fetchTrusted reads the key of t on a second connection, of t's type, and
// checks that it is the key that OpenSSH trusted.
func fetchTrusted(ctx context.Context, h Host, t trust) (hostKey, error) {
	keys, err := fetchHostKey(ctx, h, false, algorithmsFor(t.typ))
	if err != nil {
		return hostKey{}, err
	}
	k, ok := withFingerprint(keys, t.fp)
	if !ok {
		return hostKey{}, &UserError{
			Msg:  fmt.Sprintf("%s showed the host key %s, which your SSH config trusts, then %s on a second connection", h.Name, t.fp, pinOf(keys, "").Fingerprint),
			Hint: "Do not connect. Check the key on the machine: " + checkCommand(t.typ),
			Code: "host_key_mismatch", Kind: KindRefused,
		}
	}
	return k, nil
}

// wantedFamilies are the host key algorithms that fetchWanted tries, after
// ssh's own choice, to find each key that a host has: one try for each type
// of public key. Only algorithms that sign with one key share a try: RSA's.
// Each curve of ECDSA is a key of its own.
var wantedFamilies = []string{
	"ssh-ed25519",
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"rsa-sha2-512,rsa-sha2-256,ssh-rsa",
}

// fetchWanted reads h's keys, a type at a time, until one has the
// fingerprint want.
func fetchWanted(ctx context.Context, h Host, want string) (hostKey, error) {
	keys, err := fetchHostKey(ctx, h, false, "")
	if err != nil {
		return hostKey{}, err
	}
	shown := keys
	for _, algs := range wantedFamilies {
		if k, ok := withFingerprint(shown, want); ok {
			return k, nil
		}
		if slices.ContainsFunc(shown, func(k hostKey) bool { return strings.Contains(","+algs+",", ","+k.Type+",") }) {
			continue // the host showed its key of this type already
		}
		if more, err := fetchHostKey(ctx, h, false, algs); err == nil {
			shown = append(shown, more...)
		}
	}
	if k, ok := withFingerprint(shown, want); ok {
		return k, nil
	}
	return hostKey{}, &UserError{
		Msg:  fmt.Sprintf("%s shows the host key %s, not %s, which --host-key names", h.Name, keys[0].Fingerprint(), want),
		Hint: "Check the key on the machine: " + checkCommand(keys[0].Type) + ". If they differ, do not connect.",
		Code: "host_key_mismatch", Kind: KindRefused,
	}
}

// exitMaster stops the shared connection that args, a command line of
// before, names. It gets args before a pin changes, which changes the
// socket's name.
