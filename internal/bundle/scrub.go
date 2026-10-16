// Package bundle writes the debug bundle (decision 0025): one local archive
// of the debug files of the laptop and of a host, with the private values
// that it knows replaced by placeholders. Nothing is uploaded.
package bundle

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// The kinds of values that a bundle replaces.
const (
	KindHost    = "host"
	KindLaptop  = "laptop"
	KindAddr    = "addr"
	KindUser    = "user"
	KindHome    = "home"
	KindSession = "session"
	KindFolder  = "folder"
	KindEmail   = "email"
	KindKey     = "key"
	KindAlert   = "alert"
)

// kinds is the order in which replaced.txt lists the kinds.
var kinds = []string{KindHost, KindLaptop, KindAddr, KindUser, KindHome, KindSession, KindFolder, KindEmail, KindKey, KindAlert}

// kindText names a kind of value in words: one, with an article, and more.
var kindText = map[string]struct{ one, a, many string }{
	KindHost:    {"host name", "a host name", "host names"},
	KindLaptop:  {"laptop name", "the laptop's name", "laptop names"},
	KindAddr:    {"address", "an address", "addresses"},
	KindUser:    {"user name", "a user name", "user names"},
	KindHome:    {"home folder", "a home folder", "home folders"},
	KindSession: {"session name", "a session name", "session names"},
	KindFolder:  {"session folder", "a session folder", "session folders"},
	KindEmail:   {"email address", "an email address", "email addresses"},
	KindKey:     {"SSH key", "an SSH key", "SSH keys"},
	KindAlert:   {"value of the alerts file", "a value of the alerts file", "values of the alerts file"},
}

// Values are the private values that a bundle knows, and replaces.
type Values struct {
	// Hosts are names and SSH targets of hosts, such as user@my-vps:22.
	Hosts []string
	// Laptop is the laptop's name.
	Laptop []string
	// Users are user names on either side.
	Users []string
	// Homes are home folders on either side.
	Homes []string
	// Sessions and Folders are the names and folders of sessions.
	Sessions []string
	Folders  []string
	// Keys are the names of SSH keys: identity files, and the text before
	// a key's type where ssh -v names a key.
	Keys []string
}

// genericNames name nobody, so they stay: the users that
// scripts/check-private.sh allows, root and localhost, and the names of
// systems and processors, which Go's file names hold, as in os_linux.go.
var genericNames = map[string]bool{
	"me": true, "you": true, "runner": true, "shared": true, "u": true, "dev": true, "user": true,
	"linuxbrew": true, "admin": true, "ubuntu": true, "debian": true, "vscode": true, "node": true,
	"root": true, "localhost": true,
	"linux": true, "darwin": true, "windows": true, "freebsd": true, "unix": true, "amd64": true, "arm64": true,
}

// formatWords are the words of the debug records' own format: their keys
// and fixed values. They stay too, or a session called "net" would break
// every record that keytrace reads.
var formatWords = func() map[string]bool {
	words := map[string]bool{}
	for _, v := range []any{diag.Record{}, diag.Memory{}, diag.Metrics{}, diag.Proc{}} {
		t := reflect.TypeOf(v)
		for i := range t.NumField() {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			words[name] = true
			// And each part of a key such as heap_objects: the replacements
			// look inside longer names too.
			for _, part := range strings.Split(name, "_") {
				words[part] = true
			}
		}
	}
	for _, w := range diag.AllCats.List() {
		words[w] = true
	}
	for _, w := range []string{diag.EvStart, diag.EvStop, diag.EvSave, diag.EvDropped, diag.EvLog,
		"conn", "echo", "frame", "key", "late", "master", "metrics", "oom", "ping", "read", "reconnect",
		"request", "sample", "stdin", "tick", "client", "host", "bridge", "debug", "info", "warn", "error"} {
		words[w] = true
	}
	return words
}()

// productWords are the product's own names and the words of its command
// line. They stay, or a session called like the product would turn its
// name into a placeholder in every file.
var productWords = func() map[string]bool {
	words := map[string]bool{}
	for _, w := range []string{brand.Name, brand.DisplayName, brand.HostBinary, brand.VTBinary, brand.Slice,
		"setup", "host", "new", "ls", "agents", "attach", "resume", "flip", "send", "kill", "rename", "ports",
		"forward", "pull", "doctor", "debug", "config", "demo", "version", "start", "stop", "save", "profile",
		"bundle", "add", "rm", "test", "status", "create", "destroy", "restore", "idle", "alerts", "keys",
		"terminal", "keymap", "path", "install", "log", "off", "set", "completion", "help", "delete"} {
		words[strings.ToLower(w)] = true
	}
	return words
}()

// Keeps reports whether a bundle leaves a name as it is. A test of the
// command tree checks that it keeps the name of every command.
func Keeps(name string) bool { return keep(name) }

// keep reports whether a name stays as it is: a generic name, a word of
// the records' format, the product's own word, or a name under 3
// characters.
func keep(name string) bool {
	l := strings.ToLower(name)
	return len(name) < 3 || genericNames[l] || formatWords[l] || productWords[l]
}

// placed is a value and what replaces it.
type placed struct {
	value []byte
	ph    string
	kind  string
}

// Scrubber replaces the private values in the files of one bundle. One
// value gets the same placeholder in every file.
type Scrubber struct {
	homes    [][]byte          // home folders, longest first
	homesEsc []*regexp.Regexp  // the same, in a URL's escapes, in any case
	folders  []placed          // session folders as paths from ~, longest first
	keys     []placed          // names of SSH keys, longest first
	phrases  []placed          // names with a boundary inside, such as a space
	names    map[string]placed // other names, by their lower case
	inner    []placed          // the names of hosts, the laptop and users, longest first
	addrs    map[string]string // the addresses met so far, and their placeholders
	nHosts   int               // the host placeholders given so far
	counts   map[string]int    // replacements by kind
}

// NewScrubber prepares the replacements of v.
func NewScrubber(v Values) *Scrubber {
	s := &Scrubber{names: map[string]placed{}, addrs: map[string]string{}, counts: map[string]int{}}
	for _, h := range v.Homes {
		if h = filepath.Clean(h); h != "/" && h != "." {
			s.homes = append(s.homes, []byte(h))
		}
	}
	sort.SliceStable(s.homes, func(i, j int) bool { return len(s.homes[i]) > len(s.homes[j]) })
	s.homes = slices.CompactFunc(s.homes, func(a, b []byte) bool { return string(a) == string(b) })
	for _, h := range s.homes {
		s.homesEsc = append(s.homesEsc, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(strings.ReplaceAll(string(h), "/", "%2F"))))
	}
	// The laptop's name first: it wins over a host of the same name.
	for _, n := range v.Laptop {
		s.addName(n, "<laptop>", KindLaptop)
	}
	for _, n := range v.Users {
		s.addName(n, "<user>", KindUser)
	}
	for _, h := range v.Hosts {
		s.addTarget(h)
	}
	for _, n := range v.Sessions {
		if !s.known(n) {
			s.addName(n, fmt.Sprintf("<session-%d>", s.count(KindSession)+1), KindSession)
		}
	}
	s.addFolders(v.Folders)
	s.addKeys(v.Keys)
	return s
}

// known reports whether a name has a placeholder already.
func (s *Scrubber) known(n string) bool {
	_, ok := s.names[strings.ToLower(strings.TrimSpace(n))]
	return ok || slices.ContainsFunc(s.phrases, func(p placed) bool { return string(p.value) == n })
}

// count is how many distinct values of a kind have a placeholder.
func (s *Scrubber) count(kind string) int {
	n := 0
	for _, p := range s.names {
		if p.kind == kind {
			n++
		}
	}
	for _, p := range s.phrases {
		if p.kind == kind {
			n++
		}
	}
	return n
}

// addName gives a name its placeholder, unless it stays or has one.
func (s *Scrubber) addName(n, ph, kind string) {
	n = strings.TrimSpace(n)
	if keep(n) || s.known(n) {
		return
	}
	if strings.ContainsAny(n, boundaries) {
		s.phrases = append(s.phrases, placed{[]byte(n), ph, kind})
		sort.SliceStable(s.phrases, func(i, j int) bool { return len(s.phrases[i].value) > len(s.phrases[j].value) })
		return
	}
	p := placed{[]byte(n), ph, kind}
	s.names[strings.ToLower(n)] = p
	if kind == KindHost || kind == KindLaptop || kind == KindUser {
		s.inner = append(s.inner, p)
		sort.SliceStable(s.inner, func(i, j int) bool { return len(s.inner[i].value) > len(s.inner[j].value) })
	}
}

// addTarget adds a host's name or SSH target, such as user@my-vps:22: the
// user as a user, and the host as a host. An address is left to the rule
// for addresses.
func (s *Scrubber) addTarget(t string) {
	t = strings.TrimSpace(strings.TrimPrefix(t, "ssh://"))
	if u, h, ok := strings.Cut(t, "@"); ok {
		s.addName(u, "<user>", KindUser)
		t = h
	}
	if h, port, ok := strings.Cut(t, ":"); ok && !strings.Contains(port, ":") {
		t = h
	}
	t = strings.Trim(t, "[]/")
	if t == "" || isAddr(t) {
		return
	}
	s.hostPlaceholder(t)
}

// hostPlaceholder is a host's placeholder, given now if it has none.
func (s *Scrubber) hostPlaceholder(h string) string {
	if p, ok := s.names[strings.ToLower(h)]; ok {
		return p.ph
	}
	if keep(h) {
		return h
	}
	s.nHosts++
	ph := fmt.Sprintf("<host-%d>", s.nHosts)
	s.addName(h, ph, KindHost)
	return ph
}

// addFolders gives each session's folder a placeholder: ~/<folder-1> for
// one under a home folder, <folder-1> for another. A folder that is a home
// folder, or one folder at the root such as /tmp, stays.
func (s *Scrubber) addFolders(folders []string) {
	n := 0
	seen := map[string]bool{}
	for _, f := range folders {
		f = s.fromHome(filepath.Clean(strings.TrimSpace(f)))
		inHome := strings.HasPrefix(f, "~/")
		if seen[f] || !inHome && (!strings.HasPrefix(f, "/") || strings.Count(f, "/") < 2) {
			continue
		}
		seen[f] = true
		n++
		ph := fmt.Sprintf("<folder-%d>", n)
		if inHome {
			ph = "~/" + ph
		}
		s.folders = append(s.folders, placed{[]byte(f), ph, KindFolder})
	}
	sort.SliceStable(s.folders, func(i, j int) bool { return len(s.folders[i].value) > len(s.folders[j].value) })
}

// fromHome writes a path under a known home folder from ~.
func (s *Scrubber) fromHome(p string) string {
	for _, h := range s.homes {
		if p == string(h) {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, string(h)+"/"); ok {
			return "~/" + rest
		}
	}
	if m := homePattern.FindStringSubmatchIndex(p); m != nil && m[0] == 0 && !keep(p[m[4]:m[5]]) {
		return "~" + p[m[1]:]
	}
	return p
}

// addKeys gives each key's name a placeholder, both as it is and with ~
// for a home folder, or the other way round.
func (s *Scrubber) addKeys(keys []string) {
	have := map[string]bool{}
	n := 0
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if keep(k) || have[k] {
			continue
		}
		n++
		ph := fmt.Sprintf("<key-%d>", n)
		forms := []string{k, s.fromHome(k)}
		if rest, ok := strings.CutPrefix(k, "~/"); ok {
			for _, h := range s.homes {
				forms = append(forms, string(h)+"/"+rest)
			}
		}
		for _, f := range forms {
			if !have[f] {
				have[f] = true
				s.keys = append(s.keys, placed{[]byte(f), ph, KindKey})
			}
		}
	}
	sort.SliceStable(s.keys, func(i, j int) bool { return len(s.keys[i].value) > len(s.keys[j].value) })
}

// Counts are the replacements so far, by kind, in the order of
// replaced.txt. A kind with none is left out.
func (s *Scrubber) Counts() []Count {
	var out []Count
	for _, k := range kinds {
		if n := s.counts[k]; n > 0 {
			out = append(out, Count{Kind: k, N: n})
		}
	}
	return out
}
