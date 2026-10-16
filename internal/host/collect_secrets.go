package host

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The values of the host's alerts file never leave the host in a bundle
// (decision 0025). The bridge reads them from alerts.toml, and replaces
// them in each file before debug.read sends it, as alerts.Redact shows
// them: a token or a header's value becomes (hidden), and an address keeps
// only its scheme and host.

// minSecret is the shortest value that the bridge replaces. A shorter one,
// such as a header's value of "1", would change every file and names
// nobody.
const minSecret = 4

// alertValue is one value of the alerts file, and what replaces it.
type alertValue struct{ value, with []byte }

// alertSecrets reads the values to replace from the host's alerts file,
// longest first, so that a value inside another goes after it. A missing
// or broken file has none.
func alertSecrets() []alertValue {
	c, exists, err := alerts.Read(paths.AlertsConfig())
	if err != nil || !exists {
		return nil
	}
	var out []alertValue
	seen := map[string]bool{}
	add := func(v, with string) {
		if len(v) < minSecret {
			return
		}
		for _, f := range escapedForms(v) {
			if !seen[f] {
				seen[f] = true
				out = append(out, alertValue{[]byte(f), []byte(with)})
			}
		}
	}
	for _, d := range c.Destinations {
		add(d.Token, alerts.Hidden)
		add(tokenFileValue(d.TokenFile), alerts.Hidden)
		add(d.URL, alerts.RedactURL(d.URL))
		for _, v := range d.Headers {
			add(v, alerts.Hidden)
		}
		// A command can hold a token too. holder.log names a destination
		// by alerts.Target: the start of a command line.
		add(d.Run, alerts.Hidden)
		if d.Type == "command" {
			add(alerts.Target(d), alerts.Hidden)
			add(strings.TrimSuffix(alerts.Target(d), "…"), alerts.Hidden)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].value) > len(out[j].value) })
	return out
}

// escapedForms are a value as it is, and as JSON and slog's text handler
// write it in a string, once and twice. A quote, an & or a < becomes an
// escape. A holder's line goes to holder.log through slog's text handler,
// and to debug.jsonl as that text inside JSON: a quote there is escaped
// twice.
func escapedForms(v string) []string {
	quote := func(s string) string { return strings.TrimSuffix(strings.TrimPrefix(strconv.Quote(s), `"`), `"`) }
	jsonText := func(s string) string {
		j, err := json.Marshal(s)
		if err != nil {
			return s
		}
		return string(j[1 : len(j)-1])
	}
	once := []string{quote(v), jsonText(v)}
	forms := append([]string{v}, once...)
	for _, f := range once {
		forms = append(forms, quote(f), jsonText(f))
	}
	return forms
}

// httpBody is the body of an HTTP error that alerts.Dispatch logs, such as
// HTTP 401: {"error":…}, up to the end of slog's quoted value. A server can
// echo a token in it, so the host hides it.
var httpBody = regexp.MustCompile(`(HTTP [0-9]{3}(?: \([^)"\n]*\))?: )((?:[^"\\\n]|\\.)+)`)

// hideHTTPBodies hides the body of each HTTP error in b, and counts them.
func hideHTTPBodies(b []byte) ([]byte, int) {
	n := 0
	b = httpBody.ReplaceAllFunc(b, func(m []byte) []byte {
		n++
		sub := httpBody.FindSubmatch(m)
		return append(append([]byte(nil), sub[1]...), alerts.Hidden...)
	})
	return b, n
}

// tokenFileValue is the token in a destination's token file, or "".
func tokenFileValue(path string) string {
	if path == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, rest)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// replaceSecrets replaces each value in b, and hides the body of each HTTP
// error. It counts the replacements.
func replaceSecrets(b []byte, secrets []alertValue) ([]byte, int) {
	n := 0
	for _, s := range secrets {
		if c := bytes.Count(b, s.value); c > 0 {
			b = bytes.ReplaceAll(b, s.value, s.with)
			n += c
		}
	}
	b, bodies := hideHTTPBodies(b)
	return b, n + bodies
}

// collectMax is the largest file that debug.collect lists, or that
// holdsSecret reads: the limits of the debug files keep them well under it.
const collectMax = 64 << 20

// holdsSecret reports whether the file at path holds a value of the alerts
// file: for a profile, once gunzipped. The bridge leaves such a binary file
// out, because it cannot replace a value in it.
func holdsSecret(path string, secrets []alertValue) bool {
	b, err := diag.ReadPlain(path, collectMax)
	if err != nil {
		return false // debug.read cannot send it either
	}
	if strings.HasSuffix(path, ".pprof") {
		if z, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if plain, err := io.ReadAll(io.LimitReader(z, collectMax)); err == nil {
				b = plain
			}
		}
	}
	for _, s := range secrets {
		if bytes.Contains(b, s.value) {
			return true
		}
	}
	return false
}
