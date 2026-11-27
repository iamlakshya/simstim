package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The pinned host key (docs/contributing/isolation-spec.md 14.7.1, decision
// 0035). Each host reached over SSH has its own known hosts file,
// known_hosts-<name>, which holds its key under the name <brand>-<name>.
// Every ssh command for the host checks the key against that file alone,
// with options on the command line that win over every config file. The
// client never matches the user's known hosts itself: OpenSSH decides
// which key the user's own settings trust.

// PinSource says where a pinned key came from.
type PinSource string

// The places that a pin comes from, as host add prints them.
const (
	PinFromKnownHosts PinSource = "from your known_hosts"
	PinMatchedFlag    PinSource = "matched --host-key"
	PinFirstUse       PinSource = "on first use"
	// PinKept is a pin that was there before: host add keeps it.
	PinKept PinSource = "pinned before"
)

// Pin is a host's pinned key, as the client shows it.
type Pin struct {
	// Fingerprint is the SHA256 fingerprint of the key that messages show:
	// an Ed25519 key when the pin holds one.
	Fingerprint string
	// Type is that key's type, such as ssh-ed25519.
	Type string
	// Keys counts the keys of the pin.
	Keys int
	// Source says where the pin came from.
	Source PinSource
}

// Line is the pin as host add shows it, after a tick: "host key
// SHA256:… pinned, from your known_hosts".
func (p Pin) Line() string {
	more := ""
	if p.Keys > 1 {
		more = fmt.Sprintf(" (and %d more)", p.Keys-1)
	}
	switch p.Source {
	case PinKept:
		return fmt.Sprintf("host key %s%s already pinned", p.Fingerprint, more)
	case PinFirstUse:
		return fmt.Sprintf("host key %s%s pinned on first use", p.Fingerprint, more)
	case PinFromKnownHosts, PinMatchedFlag:
	}
	return fmt.Sprintf("host key %s%s pinned, %s", p.Fingerprint, more, p.Source)
}

// CheckCommand is the command that prints the pinned key's fingerprint on
// the host itself, for the user to compare.
func (p Pin) CheckCommand() string { return checkCommand(p.Type) }

// checkCommand is the command that prints the fingerprint of the host's
// key of type t on the host itself.
func checkCommand(t string) string {
	name := "ed25519"
	switch {
	case strings.HasPrefix(t, "ecdsa-"):
		name = "ecdsa"
	case t == "ssh-rsa":
		name = "rsa"
	}
	return "ssh-keygen -lf /etc/ssh/ssh_host_" + name + "_key.pub"
}

// FirstUse says what PinHostKey does with the key that a host shows, when
// neither the user's known hosts nor --host-key name it.
type FirstUse int

const (
	// FirstUseRefuse pins no key that it cannot check. PinHostKey then
	// returns ErrUnchecked.
	FirstUseRefuse FirstUse = iota
	// FirstUseAsk lets ssh show the key's fingerprint and ask the user.
	FirstUseAsk
	// FirstUseAccept pins the key that the host shows.
	FirstUseAccept
)

// ErrUnchecked says that PinHostKey found no key that it could check, and
// was not allowed to pin a key on first use.
var ErrUnchecked = errors.New("the host key is not in your known hosts, and nothing allowed a pin on first use")

// UncheckedKeyError is ErrUnchecked with the key that the host showed, as ssh
// logged it: its type and fingerprint. A caller that asks the user itself,
// such as the launcher, shows it, and pins it with that fingerprint as
// want after a yes.
type UncheckedKeyError struct{ Pin Pin }

func (e *UncheckedKeyError) Error() string { return ErrUnchecked.Error() }

// Unwrap makes errors.Is(err, ErrUnchecked) true.
func (e *UncheckedKeyError) Unwrap() error { return ErrUnchecked }

// hostKey is a public key as SSH sends it: its type, such as ssh-ed25519,
// and its wire form. The client only stores, compares and fingerprints
// keys, so it needs no more of them.
type hostKey struct {
	Type string
	Blob []byte
}

// Fingerprint is the key's SHA256 fingerprint, as ssh-keygen -l prints it.
func (k hostKey) Fingerprint() string {
	sum := sha256.Sum256(k.Blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// line is the key as a known hosts line for the host field host.
func (k hostKey) line(host string) string {
	return host + " " + k.Type + " " + base64.StdEncoding.EncodeToString(k.Blob)
}

// parseKeyLine reads one line of a known hosts file: its marker, without
// the @, and its key. It reports false for a blank line, a comment, a line
// that does not parse, a key whose wire form names another type than the
// line, and a certificate, which is never a host's own key.
func parseKeyLine(line string) (marker string, k hostKey, ok bool) {
	f := strings.Fields(line)
	if len(f) == 0 || strings.HasPrefix(f[0], "#") {
		return "", hostKey{}, false
	}
	if strings.HasPrefix(f[0], "@") {
		marker, f = f[0][1:], f[1:]
	}
	if len(f) < 3 || strings.Contains(f[1], "-cert-") {
		return "", hostKey{}, false
	}
	blob, err := base64.StdEncoding.DecodeString(f[2])
	if err != nil || len(blob) < 4 {
		return "", hostKey{}, false
	}
	n := binary.BigEndian.Uint32(blob)
	if uint64(n) > uint64(len(blob)-4) || string(blob[4:4+n]) != f[1] { //nolint:gosec // a length of at least 4, never negative
		return "", hostKey{}, false
	}
	return marker, hostKey{Type: f[1], Blob: blob}, true
}

// readKeys reads the plain keys of a known hosts file's content, once each.
func readKeys(b []byte) []hostKey {
	var keys []hostKey
	for _, l := range strings.Split(string(b), "\n") {
		if marker, k, ok := parseKeyLine(l); ok && marker == "" && !holds(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// holds reports whether keys holds k.
func holds(keys []hostKey, k hostKey) bool {
	return slices.ContainsFunc(keys, func(o hostKey) bool { return bytes.Equal(o.Blob, k.Blob) })
}

// withFingerprint returns the key of keys with the fingerprint fp.
func withFingerprint(keys []hostKey, fp string) (hostKey, bool) {
	for _, k := range keys {
		if k.Fingerprint() == fp {
			return k, true
		}
	}
	return hostKey{}, false
}

// keyRank orders key types for the fingerprint that messages show.
func keyRank(t string) int {
	switch {
	case t == "ssh-ed25519":
		return 0
	case strings.HasPrefix(t, "ecdsa-"):
		return 1
	case t == "ssh-rsa":
		return 2
	}
	return 3
}

// pinOf describes keys as a pin from src.
func pinOf(keys []hostKey, src PinSource) Pin {
	best := slices.MinFunc(keys, func(a, b hostKey) int { return keyRank(a.Type) - keyRank(b.Type) })
	return Pin{Fingerprint: best.Fingerprint(), Type: best.Type, Keys: len(keys), Source: src}
}

// keyFamily is the name that ssh gives a key's type in its messages, such
// as ED25519, from the key's type, such as ssh-ed25519.
func keyFamily(t string) string {
	switch {
	case t == "ssh-ed25519":
		return "ED25519"
	case t == "sk-ssh-ed25519@openssh.com":
		return "ED25519-SK"
	case strings.HasPrefix(t, "sk-ecdsa-"):
		return "ECDSA-SK"
	case strings.HasPrefix(t, "ecdsa-"):
		return "ECDSA"
	case t == "ssh-rsa":
		return "RSA"
	case t == "ssh-dss":
		return "DSA"
	}
	return strings.ToUpper(t)
}

// algorithmsFor are the host key algorithms that make a host show its key
// of type t, as ssh's debug log names it. A certificate's type gives the
// plain key's algorithms: the certificate signs that key.
func algorithmsFor(t string) string {
	t = strings.TrimSuffix(t, "-cert-v01@openssh.com")
	if t == "ssh-rsa" {
		return "rsa-sha2-512,rsa-sha2-256,ssh-rsa"
	}
	return t
}
