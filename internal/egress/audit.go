package egress

import (
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// MaxAuditPathBytes caps the scrubbed path in each net audit line (spec 7.2).
const MaxAuditPathBytes = 256

// maxScrubInputBytes bounds the diagnostic path and every intermediate result.
const maxScrubInputBytes = 8192

// Decoding cannot leave an encoded credential in an audit line. A deeply
// nested or oversized path gets a fixed marker instead of a partial value.
const maxScrubDecodePasses = 32

const omittedAuditPath = "[path omitted]"

// AuditEntry is one net audit record as spec 7.2 defines it. The egress
// process sends it over its control stream to the supervisor, which writes it
// to audit.jsonl.
type AuditEntry = api.NetAuditEntry

// FormatAudit fills the fixed fields of an AuditEntry and scrubs every field
// derived from the guest's request.
func FormatAudit(e AuditEntry, sc *Scrubber, now time.Time) AuditEntry {
	if now.IsZero() {
		now = time.Now()
	}
	e.V = 1
	e.T = now.UTC().Format(time.RFC3339)
	e.Kind = "net"
	secrets := sc.snapshot()
	e.Host = cleanAuditText(e.Host, secrets, 253)
	e.Method = cleanAuditText(e.Method, secrets, 16)
	e.Path = cleanAuditPath(e.Path, secrets)
	if e.Mode == "" {
		e.Mode = "splice"
	}
	if e.Decision == "" {
		e.Decision = "deny"
	}
	return e
}

// Scrubber replaces secret values in request paths and host strings with
// "[NAME]" (longest secret value first) and strips query strings, fragments
// and control characters.
type Scrubber struct {
	mu      sync.RWMutex
	secrets *auditSecrets
}

type auditSecrets struct {
	literal *strings.Replacer
	encoded *strings.Replacer
	labels  []string
}

// NewScrubber builds a Scrubber from a map of secret names to values.
func NewScrubber(secrets map[string]string) *Scrubber {
	s := &Scrubber{}
	s.SetSecrets(secrets)
	return s
}

type secretPair struct {
	needle string
	tag    string
	name   string
}

// SetSecrets replaces the active secret values in s.
func (s *Scrubber) SetSecrets(secrets map[string]string) {
	var pairs []secretPair
	seen := map[string]bool{}

	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	type namedVal struct {
		name string
		val  string
	}
	vals := make([]namedVal, 0, len(names))
	for _, name := range names {
		v := secrets[name]
		if v == "" {
			continue
		}
		vals = append(vals, namedVal{name: name, val: v})
	}
	// Longest values first so a prefix secret never masks part of a longer one.
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i].val) != len(vals[j].val) {
			return len(vals[i].val) > len(vals[j].val)
		}
		return vals[i].name < vals[j].name
	})

	add := func(needle, name string) {
		if needle == "" || seen[needle] {
			return
		}
		seen[needle] = true
		tag := "[" + api.CleanText(name, len(name)) + "]"
		pairs = append(pairs, secretPair{needle: needle, tag: tag, name: name})
	}
	for _, nv := range vals {
		add(nv.val, nv.name)
		add(url.PathEscape(nv.val), nv.name)
		add(url.QueryEscape(nv.val), nv.name)
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].needle) > len(pairs[j].needle) })

	secretsState := newAuditSecrets(pairs)
	s.mu.Lock()
	s.secrets = secretsState
	s.mu.Unlock()
}

func (s *Scrubber) snapshot() *auditSecrets {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secrets
}

// ScrubText replaces any secret value in s with "[NAME]".
func (s *Scrubber) ScrubText(text string) string {
	if s == nil || text == "" {
		return text
	}
	secrets := s.snapshot()
	if secrets == nil || secrets.literal == nil {
		return text
	}
	return secrets.literal.Replace(text)
}

// ScrubPath strips query and fragment components, replaces raw and URL-encoded
// secret values with "[NAME]", removes control and hidden characters, and
// caps the result at 256 UTF-8-safe bytes (spec 7.2).
func (s *Scrubber) ScrubPath(rawPath string) string {
	return cleanAuditPath(rawPath, s.snapshot())
}

// Invalid or incomplete escapes stay literal. One malformed byte must not
// prevent decoding a credential elsewhere in the path.
func decodeAuditEscapes(p string) (string, bool) {
	var b strings.Builder
	b.Grow(len(p))
	changed := false
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) {
			hi, hiOK := auditHex(p[i+1])
			lo, loOK := auditHex(p[i+2])
			if hiOK && loOK {
				b.WriteByte(hi<<4 | lo)
				i += 2
				changed = true
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String(), changed
}

func auditHex(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	default:
		return 0, false
	}
}

func stripQueryAndFragment(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if start, ok := auditAuthorityStart(raw); ok {
		if i := strings.IndexByte(raw[start:], '/'); i >= 0 {
			return raw[start+i:]
		}
		return "/"
	}
	return raw
}

func hasAuditAuthority(raw string) bool {
	_, ok := auditAuthorityStart(raw)
	return ok
}

func auditAuthorityStart(raw string) (int, bool) {
	if strings.HasPrefix(raw, "//") {
		return 2, true
	}
	i := strings.Index(raw, "://")
	if i < 1 {
		return 0, false
	}
	for j := range i {
		b := raw[j]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
			continue
		}
		extra := b >= '0' && b <= '9' || b == '+' || b == '-' || b == '.'
		if j == 0 || !extra {
			return 0, false
		}
	}
	return i + 3, true
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
