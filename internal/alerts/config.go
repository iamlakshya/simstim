package alerts

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Defaults for the settings a config leaves out.
const (
	DefaultDelay   = 10 * time.Second
	DefaultMinTurn = 60 * time.Second
)

// Limits on what a config may hold.
const (
	maxDestinations = 10
	maxHeaders      = 20
	maxRun          = 4096
	maxURL          = 2048
)

// Error is a problem a person can fix, with what to try.
type Error struct {
	Msg  string
	Hint string
}

func (e *Error) Error() string { return e.Msg }

// Settings is a checked config with its defaults filled in.
type Settings struct {
	Events          map[string]bool
	Delay           time.Duration
	MinTurn         time.Duration
	Detail          bool
	QuietWhenTyping bool
	Destinations    []api.AlertDestination
}

// wants reports whether any destination takes events of kind.
func (s *Settings) wants(kind string) bool {
	for i := range s.Destinations {
		if s.takes(i, kind) {
			return true
		}
	}
	return false
}

// takes reports whether destination i takes events of kind.
func (s *Settings) takes(i int, kind string) bool {
	if own := s.Destinations[i].Events; len(own) > 0 {
		for _, k := range own {
			if k == kind {
				return true
			}
		}
		return false
	}
	return s.Events[kind]
}

// Parse decodes an alerts file. A key it does not know is an error, so a typo
// never turns an alert off without a word.
func Parse(data []byte) (api.AlertsConfig, error) {
	var c api.AlertsConfig
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var sme *toml.StrictMissingError
		if errors.As(err, &sme) {
			keys := make([]string, 0, len(sme.Errors))
			for _, e := range sme.Errors {
				k := strings.Join(e.Key(), ".")
				if line, col := e.Position(); line > 0 {
					keys = append(keys, fmt.Sprintf("%s (line %d, column %d)", k, line, col))
				} else {
					keys = append(keys, k)
				}
			}
			return c, &Error{Msg: "alerts.toml: unknown key " + strings.Join(keys, ", "), Hint: "Check the spelling; docs/how-to/alerts.md lists every key."}
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			msg := strings.TrimPrefix(de.Error(), "toml: ")
			if line, col := de.Position(); line > 0 {
				msg = fmt.Sprintf("line %d, column %d: %s", line, col, msg)
			}
			return c, &Error{Msg: "alerts.toml: " + msg, Hint: "Fix the file, or write it again with `" + brand.Name + " host alerts set`."}
		}
		return c, &Error{Msg: "alerts.toml: " + err.Error(), Hint: "Fix the file, or write it again with `" + brand.Name + " host alerts set`."}
	}
	return c, nil
}

// Check validates a config and fills in its defaults. It does no I/O.
func Check(c api.AlertsConfig) (*Settings, error) {
	s := &Settings{Events: map[string]bool{}, Delay: DefaultDelay, MinTurn: DefaultMinTurn, QuietWhenTyping: true}
	events := c.Events
	if len(events) == 0 {
		events = DefaultEvents
	}
	if err := checkEvents(events, "events"); err != nil {
		return nil, err
	}
	for _, k := range events {
		s.Events[k] = true
	}
	var err error
	if s.Delay, err = duration(c.Delay, "delay", DefaultDelay, time.Hour); err != nil {
		return nil, err
	}
	if s.MinTurn, err = duration(c.MinTurn, "min_turn", DefaultMinTurn, 24*time.Hour); err != nil {
		return nil, err
	}
	if c.Detail != nil {
		s.Detail = *c.Detail
	}
	if c.QuietWhenTyping != nil {
		s.QuietWhenTyping = *c.QuietWhenTyping
	}
	if len(c.Destinations) > maxDestinations {
		return nil, &Error{Msg: fmt.Sprintf("alerts.toml has %d destinations; the most is %d", len(c.Destinations), maxDestinations), Hint: "Remove some, or send to one command that fans out."}
	}
	for i, d := range c.Destinations {
		if err := checkDestination(i, d); err != nil {
			return nil, err
		}
	}
	s.Destinations = c.Destinations
	return s, nil
}

func checkEvents(events []string, where string) error {
	for _, k := range events {
		if !knownKind(k) {
			return &Error{Msg: fmt.Sprintf("%s: unknown event %q", where, k), Hint: "Use these: " + strings.Join(Kinds, ", ") + "."}
		}
	}
	return nil
}

func duration(s, name string, def, max time.Duration) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < 0 || d > max {
		return 0, &Error{Msg: fmt.Sprintf("%s: %q is not a duration from 0s to %s", name, s, max), Hint: fmt.Sprintf("Write it like %q.", def.String())}
	}
	return d, nil
}

func checkDestination(i int, d api.AlertDestination) error {
	where := fmt.Sprintf("destination %d (%s)", i+1, d.Type)
	if err := checkEvents(d.Events, where+": events"); err != nil {
		return err
	}
	set := map[string]bool{"run": d.Run != "", "url": d.URL != "", "token": d.Token != "", "token_file": d.TokenFile != "",
		"priority": d.Priority != "", "format": d.Format != "", "headers": len(d.Headers) > 0}
	var allowed []string
	var err error
	switch d.Type {
	case "command":
		allowed = []string{"run"}
		err = checkRun(d.Run, where)
	case "ntfy":
		allowed = []string{"url", "token", "token_file", "priority"}
		err = checkNtfy(d, where)
	case "webhook":
		allowed = []string{"url", "format", "headers"}
		err = checkWebhook(d, where)
	case "":
		return &Error{Msg: fmt.Sprintf("destination %d has no type", i+1), Hint: `Set type = "command", "ntfy" or "webhook".`}
	default:
		return &Error{Msg: fmt.Sprintf("destination %d: unknown type %q", i+1, d.Type), Hint: `Set type = "command", "ntfy" or "webhook".`}
	}
	if err != nil {
		return err
	}
	for _, k := range allowed {
		delete(set, k)
	}
	for _, k := range sortedKeys(set) {
		if set[k] {
			return &Error{Msg: fmt.Sprintf("%s: %s does not apply to a %s destination", where, k, d.Type), Hint: "Remove " + k + " from it."}
		}
	}
	return nil
}

func checkRun(run, where string) error {
	if strings.TrimSpace(run) == "" {
		return &Error{Msg: where + ": run is empty", Hint: `Set run to a command line, such as run = "notify-send \"$` + brand.Env("SUMMARY") + `\"".`}
	}
	if len(run) > maxRun || strings.ContainsRune(run, 0) {
		return &Error{Msg: where + ": run is too long or holds a NUL byte", Hint: "Put a long command in a script, and run the script."}
	}
	return nil
}

func checkNtfy(d api.AlertDestination, where string) error {
	if err := CheckURL(d.URL, where); err != nil {
		return err
	}
	if d.Token != "" && d.TokenFile != "" {
		return &Error{Msg: where + ": set token or token_file, not both", Hint: "Keep token_file, and remove token."}
	}
	if strings.ContainsAny(d.Token, "\r\n\x00") {
		return &Error{Msg: where + ": the token holds a line break", Hint: "Set the token again."}
	}
	if d.TokenFile != "" && !filepath.IsAbs(expandHome(d.TokenFile)) {
		return &Error{Msg: where + ": token_file must be a full path", Hint: "Write it as ~/… or /…."}
	}
	switch strings.ToLower(d.Priority) {
	case "", "1", "2", "3", "4", "5", "min", "low", "default", "high", "max", "urgent":
		return nil
	}
	return &Error{Msg: fmt.Sprintf("%s: unknown priority %q", where, d.Priority), Hint: "Use 1 to 5, or min, low, default, high or urgent."}
}

func checkWebhook(d api.AlertDestination, where string) error {
	if err := CheckURL(d.URL, where); err != nil {
		return err
	}
	switch d.Format {
	case "", "json", "slack", "discord", "otlp":
	default:
		return &Error{Msg: fmt.Sprintf("%s: unknown format %q", where, d.Format), Hint: "Use json, slack, discord or otlp."}
	}
	if len(d.Headers) > maxHeaders {
		return &Error{Msg: where + ": too many headers", Hint: fmt.Sprintf("Keep %d at most.", maxHeaders)}
	}
	for k, v := range d.Headers {
		if !validHeaderName(k) || strings.ContainsAny(v, "\r\n\x00") {
			return &Error{Msg: fmt.Sprintf("%s: the header %q is not valid", where, k), Hint: "Use a plain header name, and a value on one line."}
		}
		switch strings.ToLower(k) {
		case "host", "content-length", "transfer-encoding", "content-type":
			return &Error{Msg: fmt.Sprintf("%s: the header %s is set for you", where, k), Hint: "Remove it from headers."}
		}
	}
	return nil
}

// CheckURL checks a destination's address: https, or http only to this host
// itself, with no user name or password in it.
func CheckURL(raw, where string) error {
	hint := "Use an https:// address (http:// only for localhost)."
	if raw == "" {
		return &Error{Msg: where + ": url is empty", Hint: hint}
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxURL || u.Host == "" || u.Opaque != "" {
		return &Error{Msg: where + ": the url is not valid", Hint: hint}
	}
	if u.User != nil {
		return &Error{Msg: where + ": the url holds a user name or password", Hint: "Remove it, and use a token instead."}
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
		return &Error{Msg: where + ": http:// sends the alert in the clear", Hint: hint}
	}
	return &Error{Msg: where + ": the url must start with https://", Hint: hint}
}

func validHeaderName(k string) bool {
	if k == "" || len(k) > 100 {
		return false
	}
	for _, r := range k {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)
		if !ok {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, rest)
		}
	}
	return p
}
