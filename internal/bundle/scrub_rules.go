package bundle

import (
	"bytes"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// How the bundle matches (decision 0025). A plain search and replace would
// break the bundle: a session called api would change internal/api in
// every stack. So a home folder or a session's folder matches only as a
// whole path, or at the start of one, and a name only as a whole token,
// with a boundary on each side.

var (
	// fingerprintRe is a key's fingerprint, as ssh -v prints it.
	fingerprintRe = regexp.MustCompile(`SHA256:[A-Za-z0-9+/]{43}`)
	// emailRe is the pattern of scripts/check-private.sh.
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// homePattern is any home folder of a real user, and homeEscaped the
	// same in a URL's escapes.
	homePattern = regexp.MustCompile(`(?i)/(Users|home)/(\pL[\pL\pN._-]*)`)
	homeEscaped = regexp.MustCompile(`(?i)%2F(Users|home)%2F(\pL[\pL\pN._-]*)`)
	// tsNetRe is a name in a tailnet.
	tsNetRe = regexp.MustCompile(`[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.ts\.net`)
	// ipv6Re and ipv4Re find candidates, which net.ParseIP must accept.
	ipv6Re = regexp.MustCompile(`(?:[0-9A-Fa-f]{0,4}:){2,7}(?:[0-9A-Fa-f]{1,4}|\d{1,3}(?:\.\d{1,3}){3})?`)
	ipv4Re = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}`)
)

// boundaries end a token: a space, a quote, @, :, =, a comma, a semicolon,
// a bracket, a backslash, and the signs that join words in a line, such as
// | or &.
const boundaries = " \t\n\r\v\f\"'`@:=,;()[]{}<>\\|+#&!?*"

func isBoundary(c byte) bool { return strings.IndexByte(boundaries, c) >= 0 }

// unicodeEscape is an escape in a JSON string, such as the one for <.
var unicodeEscape = regexp.MustCompile(`^\\u[0-9A-Fa-f]{4}`)

// boundaryLen is how many bytes of a boundary start at b[i]: an escape in a
// JSON string, such as \n, a space that is not ASCII, such as a no-break
// space, or a sign that has no width. It is 0 where no boundary starts.
func boundaryLen(b []byte, i int) int {
	switch c := b[i]; {
	case c == '\\' && unicodeEscape.Match(b[i:]):
		return 6
	case c == '\\' && i+1 < len(b) && strings.IndexByte(`ntrbf"\/`, b[i+1]) >= 0:
		return 2
	case c == '%' && i+2 < len(b) && isHex(b[i+1]) && isHex(b[i+2]):
		return 3 // an escape in a URL, such as %40 for @
	case c >= utf8.RuneSelf:
		r, n := utf8.DecodeRune(b[i:])
		if unicode.IsSpace(r) || zeroWidth(r) {
			return n
		}
		return 0
	case isBoundary(c):
		return 1
	}
	return 0
}

// zeroWidth reports whether r is a sign with no width: a zero-width space,
// joiner or non-joiner, a word joiner, or a byte order mark.
func zeroWidth(r rune) bool {
	return r >= 0x200b && r <= 0x200d || r == 0x2060 || r == 0xfeff
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isWordByte reports whether c can be part of a path's name.
func isWordByte(c byte) bool { return isAlnum(c) || c == '.' || c == '_' || c == '-' || c == '~' }

// pathStart and pathEnd report whether a path can start at b[i], and end
// at b[end]: no other character of a name next to it. A full stop that
// ends a sentence ends a path too.
func pathStart(b []byte, i int) bool { return i == 0 || !isWordByte(b[i-1]) }

func pathEnd(b []byte, end int) bool {
	if end == len(b) || !isWordByte(b[end]) {
		return true
	}
	return b[end] == '.' && (end+1 == len(b) || !isWordByte(b[end+1]))
}

// tokenStart and tokenEnd report whether a token can start at b[i], and
// end at b[end].
func tokenStart(b []byte, i int) bool { return i == 0 || isBoundary(b[i-1]) }

func tokenEnd(b []byte, end int) bool {
	if end == len(b) || isBoundary(b[end]) {
		return true
	}
	return strings.IndexByte(".!?", b[end]) >= 0 && (end+1 == len(b) || isBoundary(b[end+1]))
}

// Scrub replaces the values that s knows in b, the text of one file.
func (s *Scrubber) Scrub(b []byte) []byte {
	for _, pass := range []func([]byte) []byte{s.replaceKeys, s.replaceFingerprints, s.replaceEmails, s.replaceHomes,
		s.replaceFolders, s.replaceTSNet, s.replaceAddrs, s.replacePhrases, s.replaceTokens} {
		b = pass(b)
	}
	return b
}

// replaceBounded replaces each occurrence of p.value in b that starts and
// ends where the two functions allow.
func (s *Scrubber) replaceBounded(b []byte, p placed, start func([]byte, int) bool, end func([]byte, int) bool) []byte {
	if !bytes.Contains(b, p.value) {
		return b
	}
	var out []byte
	i := 0
	for {
		j := bytes.Index(b[i:], p.value)
		if j < 0 {
			break
		}
		j += i
		e := j + len(p.value)
		if start(b, j) && end(b, e) {
			out = append(append(out, b[i:j]...), p.ph...)
			s.counts[p.kind]++
			i = e
			continue
		}
		out = append(out, b[i:j+1]...)
		i = j + 1
	}
	return append(out, b[i:]...)
}

// replaceRe replaces each match of re that fn accepts with what fn
// returns, up to the end that fn gives.
func replaceRe(b []byte, re *regexp.Regexp, fn func(b []byte, m []int) (ph string, end int, ok bool)) []byte {
	ms := re.FindAllSubmatchIndex(b, -1)
	if ms == nil {
		return b
	}
	out := make([]byte, 0, len(b))
	last := 0
	for _, m := range ms {
		if ph, end, ok := fn(b, m); ok {
			out = append(append(out, b[last:m[0]]...), ph...)
			last = end
		}
	}
	return append(out, b[last:]...)
}

// replaceKeys replaces each key's name, also where it starts a longer
// name: ssh -v names a key's certificate as the key's path and -cert.
func (s *Scrubber) replaceKeys(b []byte) []byte {
	for _, k := range s.keys {
		b = s.replaceBounded(b, k, pathStart, endsName)
	}
	return b
}

// endsName reports whether no letter or digit stands at b[end].
func endsName(b []byte, end int) bool { return end == len(b) || !isAlnum(b[end]) }

func (s *Scrubber) replaceFingerprints(b []byte) []byte {
	return replaceRe(b, fingerprintRe, func(_ []byte, m []int) (string, int, bool) {
		s.counts[KindKey]++
		return "<fingerprint>", m[1], true
	})
}

func (s *Scrubber) replaceEmails(b []byte) []byte {
	return replaceRe(b, emailRe, func(b []byte, m []int) (string, int, bool) {
		if notEmail(b[m[0]:m[1]]) {
			return "", 0, false
		}
		s.counts[KindEmail]++
		return "<email>", m[1], true
	})
}

// unitTypes are the kinds of systemd unit. The name of a user's manager holds an
// @ and ends in .service: it is a unit, not an email address.
var unitTypes = []string{"service", "slice", "scope", "socket", "target", "mount", "automount", "timer", "path", "swap", "device"}

// notEmail reports whether a name with an @ is no email address: the name
// of a systemd unit, or one of OpenSSH's names for a key type, a cipher or
// an extension, such as those that end in @openssh.com.
func notEmail(addr []byte) bool {
	if bytes.HasSuffix(bytes.ToLower(addr), []byte("@openssh.com")) {
		return true
	}
	dot := bytes.LastIndexByte(addr, '.')
	return dot >= 0 && slices.Contains(unitTypes, string(addr[dot+1:]))
}

// replaceHomes writes each home folder as ~: the ones that s knows, and any
// /Users/<name> or /home/<name> of a name that is not generic. It does so
// in a URL's escapes too, as in %2FUsers%2Fname.
func (s *Scrubber) replaceHomes(b []byte) []byte {
	for i, h := range s.homes {
		b = s.replaceBounded(b, placed{h, "~", KindHome}, pathStart, pathEnd)
		b = replaceRe(b, s.homesEsc[i], func(_ []byte, m []int) (string, int, bool) {
			s.counts[KindHome]++
			return "~", m[1], true
		})
	}
	b = replaceRe(b, homeEscaped, func(b []byte, m []int) (string, int, bool) {
		if keep(string(b[m[4]:m[5]])) {
			return "", 0, false
		}
		s.counts[KindHome]++
		return "~", m[5], true
	})
	return replaceRe(b, homePattern, func(b []byte, m []int) (string, int, bool) {
		name := bytes.TrimRight(b[m[4]:m[5]], ".")
		end := m[4] + len(name)
		if keep(string(name)) || !pathStart(b, m[0]) {
			return "", 0, false
		}
		s.counts[KindHome]++
		return "~", end, true
	})
}

// replaceFolders replaces each session's folder at the start of a path,
// and also where it starts a longer name, as in ~/src/acme-old or
// ~/src/acme.git, which name the same project.
func (s *Scrubber) replaceFolders(b []byte) []byte {
	for _, f := range s.folders {
		b = s.replaceBounded(b, f, pathStart, endsName)
	}
	return b
}

// replaceTSNet replaces each name in a tailnet, as a host.
func (s *Scrubber) replaceTSNet(b []byte) []byte {
	return replaceRe(b, tsNetRe, func(b []byte, m []int) (string, int, bool) {
		if m[0] > 0 && (isAlnum(b[m[0]-1]) || b[m[0]-1] == '.' || b[m[0]-1] == '-') {
			return "", 0, false
		}
		s.counts[KindHost]++
		return s.hostPlaceholder(strings.ToLower(string(b[m[0]:m[1]]))), m[1], true
	})
}

// replaceAddrs replaces every IPv6 and IPv4 address but loopback and the
// unspecified address.
func (s *Scrubber) replaceAddrs(b []byte) []byte {
	b = replaceRe(b, ipv6Re, func(b []byte, m []int) (string, int, bool) {
		if m[0] > 0 && (isAlnum(b[m[0]-1]) || b[m[0]-1] == ':' || b[m[0]-1] == '.') ||
			m[1] < len(b) && (isAlnum(b[m[1]]) || b[m[1]] == ':') {
			return "", 0, false
		}
		return s.addr(b[m[0]:m[1]], m[1])
	})
	return replaceRe(b, ipv4Re, func(b []byte, m []int) (string, int, bool) {
		if m[0] > 0 && (isAlnum(b[m[0]-1]) || b[m[0]-1] == '.') || m[1] < len(b) && isAlnum(b[m[1]]) ||
			m[1]+1 < len(b) && b[m[1]] == '.' && b[m[1]+1] >= '0' && b[m[1]+1] <= '9' {
			return "", 0, false
		}
		return s.addr(b[m[0]:m[1]], m[1])
	})
}

// addr is the placeholder of an address, or false for no address, for
// loopback and for the unspecified address.
func (s *Scrubber) addr(text []byte, end int) (string, int, bool) {
	ip := parseAddr(string(text))
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", 0, false
	}
	ph, ok := s.addrs[ip.String()]
	if !ok {
		ph = fmt.Sprintf("<addr-%d>", len(s.addrs)+1)
		s.addrs[ip.String()] = ph
	}
	s.counts[KindAddr]++
	return ph, end, true
}

// isAddr reports whether t is an address, which the rule for addresses
// replaces.
func isAddr(t string) bool { return parseAddr(t) != nil }

// parseAddr reads an address as net.ParseIP does. It also reads an IPv4
// address with leading zeros, such as 010.001.002.003, which ParseIP
// refuses but some tools print.
func parseAddr(t string) net.IP {
	if ip := net.ParseIP(t); ip != nil {
		return ip
	}
	parts := strings.Split(t, ".")
	if len(parts) != 4 {
		return nil
	}
	var b [4]byte
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 8)
		if err != nil || p == "" || len(p) > 3 {
			return nil
		}
		b[i] = byte(n)
	}
	return net.IPv4(b[0], b[1], b[2], b[3])
}

func (s *Scrubber) replacePhrases(b []byte) []byte {
	for _, p := range s.phrases {
		b = s.replaceBounded(b, p, tokenStart, tokenEnd)
	}
	return b
}

// replaceTokens replaces each whole token that is a known name. A token
// ends at a boundary, so the session api matches in session=api, but not
// in internal/api.
func (s *Scrubber) replaceTokens(b []byte) []byte {
	if len(s.names) == 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if n := boundaryLen(b, i); n > 0 {
			out = append(out, b[i:i+n]...)
			i += n
			continue
		}
		j := i
		for j < len(b) && boundaryLen(b, j) == 0 {
			j++
		}
		out = append(out, s.token(b, i, j)...)
		i = j
	}
	return out
}

// token is the token b[i:j], or its placeholder.
func (s *Scrubber) token(b []byte, i, j int) []byte {
	run := b[i:j]
	if i > 0 && j < len(b) && placeholderRe.Match(b[i-1:j+1]) {
		return run // a placeholder of the bundle's own
	}
	core := bytes.TrimRight(run, ".!?")
	suffix := string(run[len(core):])
	if p, ok := s.names[strings.ToLower(string(core))]; ok {
		s.counts[p.kind]++
		return []byte(p.ph + suffix)
	}
	out, changed := string(core), false
	if strings.Contains(out, "/") {
		out, changed = s.hostPath(out)
	}
	if o, ok := s.innerNames(out); ok {
		out, changed = o, true
	}
	if changed {
		return []byte(out + suffix)
	}
	return run
}

// innerNames replaces a host's, the laptop's or a user's name inside a
// longer token, where no letter or digit stands next to it, as in
// prod-my-vps, my-vps.internal, /srv/alice-data or config-my-vps. A
// session's name stays there, as api does in fix-api: such names are
// often common words.
func (s *Scrubber) innerNames(t string) (string, bool) {
	if len(s.inner) == 0 {
		return t, false
	}
	var out strings.Builder
	changed := false
	for i := 0; i < len(t); {
		if t[i] == '<' {
			if j := strings.IndexByte(t[i:], '>'); j > 0 && placeholderRe.MatchString(t[i:i+j+1]) {
				out.WriteString(t[i : i+j+1])
				i += j + 1
				continue
			}
		}
		if i == 0 || !isAlnum(t[i-1]) {
			if p, n := s.innerAt(t, i); n > 0 {
				out.WriteString(p.ph)
				s.counts[p.kind]++
				i += n
				changed = true
				continue
			}
		}
		out.WriteByte(t[i])
		i++
	}
	return out.String(), changed
}

// innerAt is the longest name of s.inner at t[i:] that no letter or digit
// follows, and its length, or 0.
func (s *Scrubber) innerAt(t string, i int) (placed, int) {
	for _, p := range s.inner {
		n := len(p.value)
		if i+n <= len(t) && strings.EqualFold(t[i:i+n], string(p.value)) && (i+n == len(t) || !isAlnum(t[i+n])) {
			return p, n
		}
	}
	return placed{}, 0
}

// hostPath replaces the names in a token with slashes: each part that is a
// host's, the laptop's or a user's name, as in my-vps/auth-fix or
// //my-vps, and a session's name just after a host's. A session's name
// elsewhere stays, as api does in internal/api.
func (s *Scrubber) hostPath(core string) (string, bool) {
	parts := strings.Split(core, "/")
	changed := false
	for k, part := range parts {
		p, ok := s.names[strings.ToLower(part)]
		if !ok {
			continue
		}
		afterHost := k > 0 && (strings.HasPrefix(parts[k-1], "<host-") || parts[k-1] == "<laptop>")
		if p.kind == KindSession && !afterHost || p.kind == KindKey || p.kind == KindFolder {
			continue
		}
		parts[k] = p.ph
		s.counts[p.kind]++
		changed = true
	}
	return strings.Join(parts, "/"), changed
}

// rewritePath writes the home folder of a path as ~: for the file names in
// a profile.
func (s *Scrubber) rewritePath(p string) string { return string(s.replaceHomes([]byte(p))) }

// holdsHome reports whether b holds a home folder, as bytes: for a saved
// window, which the bundle does not parse.
func (s *Scrubber) holdsHome(b []byte) bool {
	for _, h := range s.homes {
		if bytes.Contains(b, h) {
			return true
		}
	}
	for _, m := range homePattern.FindAllSubmatchIndex(b, -1) {
		if !keep(string(bytes.TrimRight(b[m[4]:m[5]], "."))) {
			return true
		}
	}
	return false
}
