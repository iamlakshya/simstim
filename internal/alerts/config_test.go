package alerts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const sample = `
events = ["needs_input", "failed"]
delay = "5s"
min_turn = "2m"
detail = true

[[destination]]
type = "ntfy"
url = "https://ntfy.example.com/my-topic"
token = "tk_test"
priority = "high"

[[destination]]
type = "webhook"
url = "https://hooks.example.com/services/T000/B000/XXXX"
format = "slack"
events = ["failed"]
headers = { X-Test = "secret-value" }

[[destination]]
type = "command"
run = "cat >> ~/alerts.jsonl"
`

func TestParseAndCheck(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Check(c)
	if err != nil {
		t.Fatal(err)
	}
	if s.Delay != 5*time.Second || s.MinTurn != 2*time.Minute || !s.Detail || !s.QuietWhenTyping {
		t.Errorf("settings = %+v", s)
	}
	if !s.Events[NeedsInput] || s.Events[TurnDone] {
		t.Errorf("events = %v", s.Events)
	}
	if !s.takes(0, NeedsInput) || s.takes(1, NeedsInput) || !s.takes(1, Failed) || !s.takes(2, Failed) {
		t.Error("per-destination events are not applied")
	}
	if !s.wants(Failed) || s.wants(OOM) {
		t.Error("wants is wrong")
	}
}

func TestDefaults(t *testing.T) {
	s, err := Check(api.AlertsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Delay != DefaultDelay || s.MinTurn != DefaultMinTurn || s.Detail || !s.QuietWhenTyping {
		t.Errorf("defaults = %+v", s)
	}
	for _, k := range DefaultEvents {
		if !s.Events[k] {
			t.Errorf("default event %s missing", k)
		}
	}
	if s.Events[Exited] || s.Events[Started] {
		t.Error("exited and started are not defaults")
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantKey string
		wantPos string
	}{
		{"delai = \"5s\"\n", "delai", "line 1, column 1"},
		{"[[destination]]\ntype = \"ntfy\"\nurl = \"https://ntfy.sh/x\"\ntokn = \"tk_test\"\n", "destination.tokn", "line 4, column 1"},
		{"[other]\nx = 1\n", "other", "line 1, column 2"},
	} {
		_, err := Parse([]byte(tc.in))
		var e *Error
		if !errors.As(err, &e) || !strings.Contains(e.Msg, "unknown key") ||
			!strings.Contains(e.Msg, tc.wantKey) || !strings.Contains(e.Msg, tc.wantPos) || e.Hint == "" {
			t.Errorf("Parse(%q) = %v, want unknown key %q at %q with a hint", tc.in, err, tc.wantKey, tc.wantPos)
		}
	}
	_, err := Parse([]byte("events = [\"failed\"]\ndelay = \n"))
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Msg, "line 2") || e.Hint == "" {
		t.Errorf("syntax error = %v, want line 2 and a hint", err)
	}
}

func TestSaveRoundTripExplicitFalseBooleans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alerts.toml")
	off := false
	in := api.AlertsConfig{
		Detail:          &off,
		QuietWhenTyping: &off,
		Destinations:    []api.AlertDestination{{Type: "command", Run: "true"}},
	}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, exists, err := Read(path)
	if err != nil || !exists {
		t.Fatalf("Read = (%+v, %v, %v)", got, exists, err)
	}
	if got.Detail == nil || *got.Detail != false {
		t.Errorf("Detail = %v, want explicit false", got.Detail)
	}
	if got.QuietWhenTyping == nil || *got.QuietWhenTyping != false {
		t.Errorf("QuietWhenTyping = %v, want explicit false", got.QuietWhenTyping)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.QuietWhenTyping {
		t.Error("Load dropped quiet_when_typing = false back to default true")
	}
}

func TestCheckRejects(t *testing.T) {
	ntfy := func(f func(*api.AlertDestination)) api.AlertsConfig {
		d := api.AlertDestination{Type: "ntfy", URL: "https://ntfy.sh/topic"}
		f(&d)
		return api.AlertsConfig{Destinations: []api.AlertDestination{d}}
	}
	cases := map[string]struct {
		c    api.AlertsConfig
		want string
	}{
		"unknown event":          {api.AlertsConfig{Events: []string{"needs-input"}}, "unknown event"},
		"test is not an event":   {api.AlertsConfig{Events: []string{"test"}}, "unknown event"},
		"bad delay":              {api.AlertsConfig{Delay: "ten"}, "delay"},
		"negative delay":         {api.AlertsConfig{Delay: "-1s"}, "delay"},
		"long min_turn":          {api.AlertsConfig{MinTurn: "48h"}, "min_turn"},
		"no type":                {api.AlertsConfig{Destinations: []api.AlertDestination{{URL: "https://x.example"}}}, "no type"},
		"unknown type":           {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "email"}}}, "unknown type"},
		"plain http":             {ntfy(func(d *api.AlertDestination) { d.URL = "http://ntfy.sh/topic" }), "in the clear"},
		"ftp":                    {ntfy(func(d *api.AlertDestination) { d.URL = "ftp://ntfy.sh/topic" }), "https://"},
		"no host":                {ntfy(func(d *api.AlertDestination) { d.URL = "https:///topic" }), "not valid"},
		"user in url":            {ntfy(func(d *api.AlertDestination) { d.URL = "https://me:pw@ntfy.example.com/topic" }), "password"},
		"empty url":              {ntfy(func(d *api.AlertDestination) { d.URL = "" }), "url is empty"},
		"token and file":         {ntfy(func(d *api.AlertDestination) { d.Token, d.TokenFile = "tk_test", "/tmp/t" }), "not both"},
		"relative token file":    {ntfy(func(d *api.AlertDestination) { d.TokenFile = "token.txt" }), "full path"},
		"bad priority":           {ntfy(func(d *api.AlertDestination) { d.Priority = "loud" }), "priority"},
		"run on ntfy":            {ntfy(func(d *api.AlertDestination) { d.Run = "echo" }), "run does not apply"},
		"bad destination events": {ntfy(func(d *api.AlertDestination) { d.Events = []string{"done"} }), "unknown event"},
		"empty run":              {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "command", Run: " "}}}, "run is empty"},
		"url on command":         {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "command", Run: "true", URL: "https://x.example"}}}, "url does not apply"},
		"bad format":             {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "webhook", URL: "https://x.example", Format: "teams"}}}, "format"},
		"bad header":             {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "webhook", URL: "https://x.example", Headers: map[string]string{"X-A": "a\r\nB: c"}}}}, "header"},
		"token on webhook":       {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "webhook", URL: "https://x.example", Token: "tk_test"}}}, "token does not apply"},
		"reserved header":        {api.AlertsConfig{Destinations: []api.AlertDestination{{Type: "webhook", URL: "https://x.example", Headers: map[string]string{"Host": "x"}}}}, "set for you"},
	}
	for name, tc := range cases {
		_, err := Check(tc.c)
		var e *Error
		if !errors.As(err, &e) || !strings.Contains(e.Msg, tc.want) || e.Hint == "" {
			t.Errorf("%s: Check = %v, want an error with %q and a hint", name, err, tc.want)
		}
	}
}

func TestCheckAllowsLocalHTTP(t *testing.T) {
	for _, u := range []string{"http://localhost:8080/t", "http://127.0.0.1/t", "http://[::1]:9/t", "https://ntfy.sh/t"} {
		if err := CheckURL(u, "test"); err != nil {
			t.Errorf("CheckURL(%q) = %v", u, err)
		}
	}
}

func TestSaveReadLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "alerts.toml")
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	back, exists, err := Read(path)
	if err != nil || !exists {
		t.Fatalf("Read = %v, %v", exists, err)
	}
	if len(back.Destinations) != 3 || back.Destinations[1].Headers["X-Test"] != "secret-value" || *back.Detail != true {
		t.Errorf("round trip lost data: %+v", back)
	}
	if s, err := Load(path); err != nil || s == nil || len(s.Destinations) != 3 {
		t.Errorf("Load = %v, %v", s, err)
	}
	// A broken config is refused before it is written.
	if err := Save(path, api.AlertsConfig{Events: []string{"nope"}}); err == nil {
		t.Error("Save wrote a config that does not check")
	}
	if s, err := Load(filepath.Join(dir, "missing.toml")); s != nil || err != nil {
		t.Errorf("a missing file must mean off: %v, %v", s, err)
	}
}

func TestLoadRefusesReadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "alerts.toml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Hint, "chmod 600 "+path) {
		t.Errorf("Load of a 0644 file = %v, want a chmod hint", err)
	}

	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("tk_test\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := "[[destination]]\ntype = \"ntfy\"\nurl = \"https://ntfy.sh/t\"\ntoken_file = \"" + tok + "\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.As(err, &e) || !strings.Contains(e.Hint, "chmod 600 "+tok) {
		t.Errorf("Load with a group-readable token file = %v, want a chmod hint", err)
	}
	if err := os.Chmod(tok, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load with a private token file = %v", err)
	}
}

func TestMerge(t *testing.T) {
	yes := true
	base := api.AlertsConfig{Delay: "5s", Events: []string{Failed}, Destinations: []api.AlertDestination{{Type: "command", Run: "a"}}}
	add := api.AlertsConfig{Detail: &yes, Destinations: []api.AlertDestination{{Type: "command", Run: "b"}}}
	m := Merge(base, add)
	if m.Delay != "5s" || len(m.Events) != 1 || m.Detail == nil || !*m.Detail || len(m.Destinations) != 2 || m.Destinations[1].Run != "b" {
		t.Errorf("Merge = %+v", m)
	}
	if len(base.Destinations) != 1 {
		t.Error("Merge changed its input")
	}
}

func TestRedact(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	r := Redact(c)
	all := strings.Join([]string{r.Destinations[0].URL, r.Destinations[0].Token, r.Destinations[1].URL, r.Destinations[1].Headers["X-Test"]}, " ")
	for _, secret := range []string{"tk_test", "my-topic", "XXXX", "secret-value", "services"} {
		if strings.Contains(all, secret) {
			t.Errorf("redacted config still shows %q: %s", secret, all)
		}
	}
	if r.Destinations[0].URL != "https://ntfy.example.com/…" || r.Destinations[0].Token != Hidden {
		t.Errorf("redacted ntfy = %+v", r.Destinations[0])
	}
	if c.Destinations[0].Token != "tk_test" {
		t.Error("Redact changed its input")
	}
	if RedactURL("https://ntfy.sh") != "https://ntfy.sh" || RedactURL("https://x.example/?k=v") != "https://x.example/…" {
		t.Error("RedactURL is wrong")
	}
}
