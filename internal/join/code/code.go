// Package code defines checked invitation and reply codes. It holds no secrets
// and opens no connection. The owner compares fingerprints on both screens.
package code

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Version is the closed TLV wire format understood by this release.
const Version byte = 1

// MaxCode is the largest accepted textual invite or reply, including separators.
const MaxCode = 4096
const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
const (
	fieldKey         byte = 1
	fieldNonce       byte = 2
	fieldExpiry      byte = 3
	fieldAccount     byte = 4
	fieldAddress     byte = 5
	fieldPort        byte = 6
	fieldFingerprint byte = 7
	fieldTier        byte = 8
	fieldHop         byte = 9
)

// Error describes a failed code or preparation check without including a code.
type Error struct{ Code, Message, Hint string }

func (e *Error) Error() string { return e.Message + "\nTry: " + e.Hint }
func invalid(message string) error {
	return &Error{"code_check", message, "Copy the code again through a channel you trust and compare both screens."}
}

// Invite carries no secret. Names and labels remain on the laptop.
type Invite struct {
	PublicKey string
	Nonce     uint64
	Expires   time.Time
}

// Hop is one SSH server on the path, nearest the laptop first. The final
// server is described by Reply's account, addresses, port and fingerprints.
type Hop struct {
	Account      string
	Addresses    []string
	Port         uint16
	Fingerprints []string
}

// Reply binds the machine's connection facts to an open invite's nonce.
type Reply struct {
	Nonce         uint64
	Account       string
	Addresses     []string
	Port          uint16
	Fingerprints  []string
	SuggestedTier string
	Hops          []Hop
}

// ValidAccount applies the same non-root account rule as the root step.
func ValidAccount(s string) bool { return s != "root" && accountRE.MatchString(s) }

var accountRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var addressRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var fingerprintRE = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// ValidAddress accepts an IP literal or a plain DNS name, never SSH syntax.
func ValidAddress(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if !addressRE.MatchString(s) || len(s) > 253 || strings.Contains(s, "..") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

// ValidFingerprint accepts canonical SHA-256 host-key fingerprints only.
func ValidFingerprint(s string) bool {
	if !fingerprintRE.MatchString(s) {
		return false
	}
	b, e := base64.RawStdEncoding.DecodeString(s[7:])
	return e == nil && len(b) == 32 && base64.RawStdEncoding.EncodeToString(b) == s[7:]
}

// PublicKey checks and canonicalises one unadorned SSH public key.
func PublicKey(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", invalid("the public key contains an invalid control character")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || strings.ContainsAny(comment, "\r\n") {
		return "", invalid("the invite needs one plain SSH public key")
	}
	if _, cert := key.(*ssh.Certificate); cert {
		return "", invalid("the invite needs a public key, rather than a certificate")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}
func (i Invite) validate(now time.Time) error {
	if _, err := PublicKey(i.PublicKey); err != nil {
		return err
	}
	if i.Nonce == 0 || i.Expires.IsZero() || i.Expires.Unix() <= 0 {
		return invalid("the invite is missing a nonce or expiry")
	}
	if !now.IsZero() && !now.Before(i.Expires) {
		return &Error{"invite_expired", "the invite has expired", "Make another invite on the laptop."}
	}
	return nil
}

// ValidateConnection checks untrusted account, address and port fields without
// inventing a host key or granting any connection permission.
func ValidateConnection(account string, addresses []string, port uint16) error {
	if !ValidAccount(account) || port == 0 || len(addresses) == 0 || len(addresses) > 16 {
		return invalid("the reply has an invalid account, port or address list")
	}
	for _, address := range addresses {
		if !ValidAddress(address) {
			return invalid("the reply has an invalid address")
		}
	}
	if hasDuplicate(addresses) {
		return invalid("the reply repeats an address")
	}
	return nil
}

// Validate checks the closed routing fields and real host keys of one hop.
func (h Hop) Validate() error { return validateHop(h) }
func validateHop(h Hop) error {
	if err := ValidateConnection(h.Account, h.Addresses, h.Port); err != nil {
		return err
	}
	if len(h.Fingerprints) == 0 || len(h.Fingerprints) > 8 {
		return invalid("the reply has an invalid host-key list")
	}
	for _, fingerprint := range h.Fingerprints {
		if !ValidFingerprint(fingerprint) {
			return invalid("the reply has an invalid host-key fingerprint")
		}
	}
	if hasDuplicate(h.Fingerprints) {
		return invalid("the reply repeats a host key")
	}
	return nil
}

func hasDuplicate(v []string) bool {
	for n, s := range v {
		if slices.Contains(v[:n], s) {
			return true
		}
	}
	return false
}

// Validate checks all reply fields before a caller writes any configuration.
func (r Reply) Validate() error {
	if r.Nonce == 0 || len(r.Hops) > 4 {
		return invalid("the reply has an invalid nonce or SSH path")
	}
	if err := validateHop(Hop{r.Account, r.Addresses, r.Port, r.Fingerprints}); err != nil {
		return err
	}
	if r.SuggestedTier != "" && r.SuggestedTier != "own" && r.SuggestedTier != "shared" && r.SuggestedTier != "throwaway" {
		return invalid("the reply has an unknown tier suggestion")
	}
	for _, h := range r.Hops {
		if err := validateHop(h); err != nil {
			return err
		}
	}
	return nil
}
func addField(b *bytes.Buffer, t byte, v []byte) {
	b.WriteByte(t)
	_ = binary.Write(b, binary.BigEndian, uint16(len(v)))
	b.Write(v)
}
func number(n uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, n); return b }
func hopFields(b *bytes.Buffer, h Hop) {
	addField(b, fieldAccount, []byte(h.Account))
	for _, a := range h.Addresses {
		addField(b, fieldAddress, []byte(a))
	}
	p := []byte{byte(h.Port >> 8), byte(h.Port)}
	addField(b, fieldPort, p)
	for _, f := range h.Fingerprints {
		addField(b, fieldFingerprint, []byte(f))
	}
}

// EncodeInvite creates the checked base32 form of an invite.
func EncodeInvite(i Invite) (string, error) {
	if err := i.validate(time.Time{}); err != nil {
		return "", err
	}
	key, err := PublicKey(i.PublicKey)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteByte(Version)
	addField(&b, fieldKey, []byte(key))
	addField(&b, fieldNonce, number(i.Nonce))
	addField(&b, fieldExpiry, number(uint64(i.Expires.Unix())))
	return encode(b.Bytes())
}

// EncodeReply creates the checked base32 form of a validated reply.
func EncodeReply(r Reply) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteByte(Version)
	addField(&b, fieldNonce, number(r.Nonce))
	hopFields(&b, Hop{r.Account, r.Addresses, r.Port, r.Fingerprints})
	if r.SuggestedTier != "" {
		addField(&b, fieldTier, []byte(r.SuggestedTier))
	}
	for _, h := range r.Hops {
		var hb bytes.Buffer
		hopFields(&hb, h)
		addField(&b, fieldHop, hb.Bytes())
	}
	return encode(b.Bytes())
}
func encode(b []byte) (string, error) {
	raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	var groups []string
	for len(raw) > 0 {
		n := min(13, len(raw))
		s := raw[:n]
		groups = append(groups, s+string(check(s)))
		raw = raw[n:]
	}
	out := strings.Join(groups, "-")
	if len(out) > MaxCode {
		return "", invalid("the code is longer than 4 KiB")
	}
	return out, nil
}

// check uses the Syncthing device-ID convention: alternate weights 1 and 2
// from the left, folding each product in base 32. Each group is independent.
func check(s string) byte {
	total := 0
	for position := 0; position < len(s); position++ {
		product := strings.IndexByte(alphabet, s[position]) * (1 + position%2)
		total += product/32 + product%32
	}
	return alphabet[(32-total%32)%32]
}

func decode(code string) ([]byte, error) {
	if len(code) > MaxCode {
		return nil, invalid("the code is longer than 4 KiB")
	}
	// Separators may wrap a pasted code. Other punctuation is never ignored.
	code = strings.ToUpper(strings.TrimSpace(code))
	groups := strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == ' ' || r == '\t' || r == '\r' || r == '\n' })
	if len(groups) == 0 {
		return nil, invalid("the code is empty")
	}
	var raw strings.Builder
	for n, g := range groups {
		if len(g) < 2 || len(g) > 14 || n < len(groups)-1 && len(g) != 14 {
			return nil, invalid(fmt.Sprintf("code group %d has the wrong length", n+1))
		}
		for _, c := range g {
			if !strings.ContainsRune(alphabet, c) {
				return nil, invalid(fmt.Sprintf("code group %d contains an invalid character", n+1))
			}
		}
		if check(g[:len(g)-1]) != g[len(g)-1] {
			return nil, invalid(fmt.Sprintf("code group %d failed its check", n+1))
		}
		raw.WriteString(g[:len(g)-1])
	}
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(raw.String())
	if err != nil || len(b) < 1 || base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b) != raw.String() {
		return nil, invalid("the code has invalid base32 padding bits")
	}
	if b[0] != Version {
		return nil, &Error{"code_version", "the code uses a format this release does not understand", "Update the CLI on both machines, then make a fresh invite."}
	}
	return b[1:], nil
}

type field struct {
	typ  byte
	data []byte
}

func fields(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		if len(b) < 3 {
			return nil, invalid("the code ends inside a field")
		}
		n := int(binary.BigEndian.Uint16(b[1:3]))
		if n == 0 || n > len(b)-3 {
			return nil, invalid("the code has an invalid field length")
		}
		out = append(out, field{b[0], b[3 : 3+n]})
		b = b[3+n:]
	}
	return out, nil
}

// DecodeInvite checks groups, the closed version and fields, then expiry.
func DecodeInvite(code string, now time.Time) (Invite, error) {
	var i Invite
	b, err := decode(code)
	if err != nil {
		return i, err
	}
	fs, err := fields(b)
	if err != nil {
		return i, err
	}
	seen := map[byte]bool{}
	for _, f := range fs {
		if seen[f.typ] {
			return i, invalid("the invite repeats a field")
		}
		seen[f.typ] = true
		switch f.typ {
		case fieldKey:
			i.PublicKey = string(f.data)
		case fieldNonce:
			if len(f.data) != 8 {
				return i, invalid("the invite has an invalid nonce")
			}
			i.Nonce = binary.BigEndian.Uint64(f.data)
		case fieldExpiry:
			if len(f.data) != 8 {
				return i, invalid("the invite has an invalid expiry")
			}
			n := binary.BigEndian.Uint64(f.data)
			if n > 1<<63-1 {
				return i, invalid("the invite has an invalid expiry")
			}
			i.Expires = time.Unix(int64(n), 0).UTC()
		default:
			return i, invalid("the invite has an unknown field")
		}
	}
	key, err := PublicKey(i.PublicKey)
	if err != nil {
		return i, err
	}
	i.PublicKey = key
	return i, i.validate(now)
}
func parseHop(fs []field, allowExtra bool) (Hop, error) {
	var h Hop
	seen := map[byte]bool{}
	for _, f := range fs {
		if f.typ == fieldAddress || f.typ == fieldFingerprint {
		} else if seen[f.typ] {
			return h, invalid("the reply repeats a field")
		}
		seen[f.typ] = true
		switch f.typ {
		case fieldAccount:
			h.Account = string(f.data)
		case fieldAddress:
			h.Addresses = append(h.Addresses, string(f.data))
		case fieldPort:
			if len(f.data) != 2 {
				return h, invalid("the reply has an invalid port")
			}
			h.Port = binary.BigEndian.Uint16(f.data)
		case fieldFingerprint:
			h.Fingerprints = append(h.Fingerprints, string(f.data))
		default:
			if !allowExtra {
				return h, invalid("the SSH hop has an unknown field")
			}
		}
	}
	return h, validateHop(h)
}

// DecodeReply checks all lengths and fields before returning connection facts.
func DecodeReply(code string) (Reply, error) {
	var r Reply
	b, err := decode(code)
	if err != nil {
		return r, err
	}
	fs, err := fields(b)
	if err != nil {
		return r, err
	}
	var main []field
	seen := map[byte]bool{}
	for _, f := range fs {
		switch f.typ {
		case fieldNonce, fieldTier:
			if seen[f.typ] {
				return r, invalid("the reply repeats a field")
			}
			seen[f.typ] = true
			if f.typ == fieldNonce {
				if len(f.data) != 8 {
					return r, invalid("the reply has an invalid nonce")
				}
				r.Nonce = binary.BigEndian.Uint64(f.data)
			} else {
				r.SuggestedTier = string(f.data)
			}
		case fieldAccount, fieldAddress, fieldPort, fieldFingerprint:
			main = append(main, f)
		case fieldHop:
			nested, e := fields(f.data)
			if e != nil {
				return r, e
			}
			h, e := parseHop(nested, false)
			if e != nil {
				return r, e
			}
			r.Hops = append(r.Hops, h)
		default:
			return r, invalid("the reply has an unknown field")
		}
	}
	h, err := parseHop(main, false)
	if err != nil {
		return r, err
	}
	r.Account, r.Addresses, r.Port, r.Fingerprints = h.Account, h.Addresses, h.Port, h.Fingerprints
	return r, r.Validate()
}
