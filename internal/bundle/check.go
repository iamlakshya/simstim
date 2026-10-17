package bundle

import (
	"bytes"
	"regexp"
	"strings"
)

// The last check (decision 0025). After the replacements, the bundle looks
// in each file again, and leaves out one that still holds a private value.
// The check does not run the replacements' rules a second time, which by
// then find nothing. It searches, in any case, for each value that the
// bundle knows, unless the value is plainly part of a longer word. What
// counts as a longer word depends on the kind of value (passesFor). It
// also runs the patterns again: home folders, email addresses, tailnet
// names, fingerprints and addresses.

// placeholderRe matches what the replacements wrote, which the check skips.
var placeholderRe = regexp.MustCompile(`<(?:host|addr|session|key|folder)-[0-9]+>|<(?:laptop|user|email|fingerprint|ssh-config|known-hosts)>|\(hidden\)`)

// checkMode says what a check looks at.
type checkMode int

const (
	checkText     checkMode = iota // a text file: every value
	checkFileName                  // a profile's file name: a host may share a name with a Go package
	checkNoNames                   // another string of a profile, such as a function's name: no names
)

// Check reports the first kind of private value that b still holds, or "".
func (s *Scrubber) Check(b []byte) string { return s.check(b, checkText) }

// checkPath is Check for a profile's string: a file name, or another
// string, such as a function's name, which stays as it is.
func (s *Scrubber) checkPath(str string, fileName bool) string {
	if fileName {
		return s.check([]byte(str), checkFileName)
	}
	return s.check([]byte(str), checkNoNames)
}

func (s *Scrubber) check(b []byte, mode checkMode) string {
	low := bytes.ToLower(placeholderRe.ReplaceAll(b, nil))
	for _, h := range s.homes {
		lh := bytes.ToLower(h)
		if holds(low, lh, pathPasses) || holds(low, bytes.ReplaceAll(lh, []byte("/"), []byte("%2f")), pathPasses) {
			return KindHome
		}
	}
	for _, list := range [][]placed{s.folders, s.keys} {
		for _, p := range list {
			if holds(low, bytes.ToLower(p.value), pathPasses) {
				return p.kind
			}
		}
	}
	if mode != checkNoNames {
		if k := s.checkNames(low, mode); k != "" {
			return k
		}
	}
	for a := range s.addrs {
		if holds(low, []byte(strings.ToLower(a)), addrPasses) {
			return KindAddr
		}
	}
	return s.checkPatterns(low)
}

// checkNames looks for each name that the bundle knows.
func (s *Scrubber) checkNames(low []byte, mode checkMode) string {
	for _, p := range s.phrases {
		if holds(low, bytes.ToLower(p.value), passesFor(p.kind, mode)) {
			return p.kind
		}
	}
	for l, p := range s.names {
		if holds(low, []byte(l), passesFor(p.kind, mode)) {
			return p.kind
		}
	}
	return ""
}

// checkPatterns runs the replacements' patterns once more, on a copy.
func (s *Scrubber) checkPatterns(low []byte) string {
	c := &Scrubber{addrs: map[string]string{}, counts: map[string]int{}}
	for _, pass := range []func([]byte) []byte{c.replaceFingerprints, c.replaceEmails, c.replaceHomes, c.replaceAddrs} {
		pass(bytes.Clone(low))
	}
	if tsNetRe.Match(low) {
		return KindHost
	}
	for _, k := range kinds {
		if c.counts[k] > 0 {
			return k
		}
	}
	return ""
}

// A passes rule says whether a value, found between the characters prev
// and next, is part of a longer word, so that the check passes it by. Each
// is -1 at an end of the text.
type passes func(prev, next int) bool

func alnumAt(c int) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// pathPasses is the rule for a home folder, a session's folder and a key's
// path: one passes only when a letter or a digit follows it, as in
// ~/src/app2. ~/src/app-old and ~/.ssh/key-cert still name it.
func pathPasses(_, next int) bool { return alnumAt(next) }

// addrPasses is the rule for an address: a letter, a digit, a colon or a
// full stop next to it.
func addrPasses(prev, next int) bool {
	in := func(c int) bool { return alnumAt(c) || c == ':' || c == '.' }
	return in(prev) || in(next)
}

// passesFor is the rule for a name of a kind. A session's name passes next
// to a slash or a hyphen too, as api does in internal/api or fix-api: such
// names are often common words. A user's, a host's or the laptop's name
// passes only inside a longer word of letters and digits, so prod-my-vps
// and alice-data keep their file out. In a profile's file names, a host's
// name passes too as a whole part of a path, as a Go package's name does
// in internal/api/conn.go.
func passesFor(kind string, mode checkMode) passes {
	in := func(c int) bool { return alnumAt(c) || c == '/' || c == '-' }
	switch {
	case kind == KindSession:
		return func(prev, next int) bool { return in(prev) || in(next) }
	case kind == KindHost && mode == checkFileName:
		edge := func(c int) bool { return c < 0 || c == '/' }
		return func(prev, next int) bool { return alnumAt(prev) || alnumAt(next) || edge(prev) && edge(next) }
	}
	return func(prev, next int) bool { return alnumAt(prev) || alnumAt(next) }
}

// holds reports whether hay holds v somewhere that the rule does not pass.
func holds(hay, v []byte, rule passes) bool {
	if len(v) == 0 {
		return false
	}
	for i := 0; ; {
		j := bytes.Index(hay[i:], v)
		if j < 0 {
			return false
		}
		j += i
		prev, next := -1, -1
		if j > 0 {
			prev = int(hay[j-1])
		}
		if end := j + len(v); end < len(hay) {
			next = int(hay[end])
		}
		if !rule(prev, next) {
			return true
		}
		i = j + 1
	}
}
