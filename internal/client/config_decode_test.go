package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every ordinary loader refuses ambiguous struct aliases before it can
// choose a setting or a route in map order. A mixed-case single key works.
func TestConfigLoadersRejectAliases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, file, body, conflict string
	}{
		{"settings field", "config.toml", "[ui]\nMotion = 'on'\nMOTION = 'off'\n", `both "MOTION" and "Motion" set "motion" in [ui]`},
		{"settings table", "config.toml", "[UI]\nmotion = 'on'\n[ui]\nmotion = 'off'\n", `both "UI" and "ui" set "ui"`},
		{"host name field", "hosts.toml", "[[host]]\nName = 'one'\nNAME = 'two'\nssh = 'route'\n", `both "NAME" and "Name" set "name" in [host]`},
		{"host route field", "hosts.toml", "[[host]]\nname = 'one'\nSSH = 'one'\nSsh = 'two'\n", `both "SSH" and "Ssh" set "ssh" in [host]`},
		{"host arrays", "hosts.toml", "[[HOST]]\nname = 'one'\nssh = 'one'\n[[host]]\nname = 'two'\nssh = 'two'\n", `both "HOST" and "host" set "host"`},
		{"nested machine", "hosts.toml", "[[host]]\nname = 'one'\n[host.machine]\nProvider = 'docker'\nPROVIDER = 'lima'\nid = 'one'\n", `both "PROVIDER" and "Provider" set "provider" in [host.machine]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(ConfigDir(), tt.file)
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			var first string
			for range 64 {
				var message string
				if tt.file == "config.toml" {
					s, warning := ReadSettings()
					message = warning
					if !reflect.DeepEqual(s, DefaultSettings()) || warning == "" {
						t.Fatalf("ambiguous settings loaded as %+v with warning %q", s, warning)
					}
				} else {
					hosts, err := LoadHosts()
					var user *UserError
					if len(hosts) != 0 || !errors.As(err, &user) || user.Code != "bad_config" || user.Kind != KindUsage || user.Hint == "" {
						t.Fatalf("ambiguous routes loaded as %+v, %v", hosts, err)
					}
					message = err.Error()
				}
				if !strings.Contains(message, tt.conflict) {
					t.Fatalf("error %q, want %q", message, tt.conflict)
				}
				if first != "" && message != first {
					t.Fatalf("the same file gave %q and %q", first, message)
				}
				first = message
			}
			var dst any = &Settings{}
			if tt.file == "hosts.toml" {
				dst = &hostsFile{}
			}
			if check := fileCheck(tt.file, path, dst); check.OK || !strings.Contains(check.Detail, tt.conflict) || check.Fix == "" {
				t.Fatalf("doctor accepted aliases: %+v", check)
			}
		})
	}
}

// Loader preflight leaves unknown data alone and preserves every exact
// free-map key, host-name value and independently scoped array element.
func TestConfigLoadersKeepExactNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := "[UI]\nMotion = 'off'\n[secrets]\nToken = 'printf one'\nTOKEN = 'printf two'\n[unknown]\nMotion = 'one'\nMOTION = 'two'\n"
	if err := os.WriteFile(settingsPath(), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warning := ReadSettings()
	if warning != "" || s.UI.Motion != "off" || len(s.Secrets) != 2 || s.Secrets["Token"] != "printf one" || s.Secrets["TOKEN"] != "printf two" {
		t.Fatalf("valid settings changed: %+v, %q", s, warning)
	}
	hosts := "[[HOST]]\nName = 'Box'\nSSH = 'one'\nlabels = { Name = 'one', NAME = 'two' }\n[[HOST]]\nNAME = 'box'\nSsh = 'two'\nunknown = { SSH = 'one', Ssh = 'two' }\n"
	if err := os.WriteFile(hostsPath(), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHosts()
	if err != nil || len(h) != 2 || h[0].Name != "Box" || h[1].Name != "box" || h[0].SSH != "one" || h[1].SSH != "two" || len(h[0].Labels) != 2 {
		t.Fatalf("exact names changed: %+v, %v", h, err)
	}
	if check := fileCheck("config.toml", settingsPath(), &Settings{}); check.OK || !strings.Contains(check.Detail, "unknown keys") {
		t.Fatalf("doctor lost unknown-key policy: %+v", check)
	}
}

// The shared schema view agrees with the pinned decoder for embedding,
// tags and declaration-ordered EqualFold fallback. No lowercase shortcut
// can replace that rule when two distinct tags differ only in case.
func TestConfigFieldResolutionMatchesDecoder(t *testing.T) {
	type Base struct{ Value string }
	type Other struct{ Value string }
	type Tagged struct {
		Value string `toml:"Value"`
	}
	types := []any{
		struct {
			Untagged string
			hidden   string
			Skip     string `toml:"-"`
		}{},
		struct{ *Base }{},
		struct {
			Base
			Value string
		}{},
		struct {
			Base
			Other
		}{},
		struct {
			Base
			Tagged
		}{},
		struct {
			Base `toml:"nested"`
		}{},
		struct {
			First  string `toml:"foo"`
			Second string `toml:"Foo"`
		}{},
		struct {
			First  string `toml:"key,omitempty"`
			Second string `toml:"-,omitempty"`
		}{},
		struct {
			Letter string `toml:"σ"`
		}{},
	}
	for _, value := range types {
		typ := reflect.TypeOf(value)
		for _, key := range []string{"Untagged", "UNTAGGED", "hidden", "Skip", "Value", "VALUE", "value", "nested", "FOO", "Foo", "foo", "key", "KEY", "σ", "ς", "Σ", "-"} {
			data := toml.Key{key}.String() + " = 'sentinel'\n"
			decoded := reflect.New(typ)
			md, err := toml.Decode(data, decoded.Interface())
			if err != nil {
				// A named embedded table cannot accept a string. It still
				// has a known field, as the shared view must report.
				if key != "nested" || !strings.Contains(err.Error(), "expected table") {
					t.Fatal(err)
				}
			}
			view := InspectConfigTable(map[string]any{key: "sentinel"}, typ)
			row := view.Keys[0]
			known := len(md.Undecoded()) == 0 || err != nil
			if (row.Type != nil) != known {
				t.Fatalf("%s key %q: decoder known %v, inspector %+v", typ, key, known, row)
			}
			if row.Type != nil && row.Type.Kind() == reflect.String {
				var expected reflect.Value
				for _, field := range configFields(typ) {
					if field.name == row.Name {
						expected = decoded.Elem().FieldByIndex(field.index)
					}
				}
				if !expected.IsValid() || expected.String() != "sentinel" {
					t.Fatalf("%s key %q resolved to %q, which decoder left empty", typ, key, row.Name)
				}
			}
		}
	}
}
