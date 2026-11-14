package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TOML spellings may match a struct field in any case, but two spellings
// must not choose values or discard a table according to map order.
func TestConfigCheckCanonicalCollisions(t *testing.T) {
	for _, tt := range []struct {
		name, file, body, message string
		line                      int
	}{
		{"setting", "config.toml", "[ui]\nMotion = 'on'\nMOTION = 'off'\n", `both "MOTION" and "Motion" set "motion" in [ui]`, 2},
		{"table", "config.toml", "[UI]\nMotion = 'on'\n[ui]\nmotion = 'off'\n", `both "UI" and "ui" set "ui"`, 3},
		{"host arrays", "hosts.toml", "[[HOST]]\nname = 'box'\nssh = 'one'\n[[host]]\nname = 'box'\nssh = 'two'\n", `both "HOST" and "host" set "host"`, 4},
		{"host name keys", "hosts.toml", "[[host]]\nName = 'good'\nNAME = 'bad space'\nssh = 'box'\n", `both "NAME" and "Name" set "name" in [[host]]`, 2},
		{"nested table", "hosts.toml", "[[host]]\nname = 'box'\n[host.Machine]\nprovider = 'docker'\nid = 'one'\n[host.machine]\nprovider = 'docker'\nid = 'two'\n", `both "Machine" and "machine" set "machine" in [[host]]`, 6},
		{"inline", "config.toml", "ui = { motion = 'on', Motion = 'off' }\n", `both "Motion" and "motion" set "motion" in [ui]`, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			write := configHome(t)
			write(tt.file, tt.body)
			var first string
			for range 64 {
				r := checkConfig()
				found := false
				for _, f := range r.Findings {
					if f.Message == tt.message {
						found = true
						if f.Code != "bad_value" || f.Severity != severityError || f.Line != tt.line {
							t.Errorf("collision provenance = %+v, want line %d", f, tt.line)
						}
					}
				}
				if !found || r.OK {
					t.Fatalf("collision was accepted: %+v, want %s", r, tt.message)
				}
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if first == "" {
					first = string(b)
				} else if string(b) != first {
					t.Fatalf("the same file gave different reports:\n%s\n%s", first, b)
				}
			}
		})
	}
}

// Free-map keys and host-name values are exact names, not struct fields.
// Case twins must survive the check as distinct names.
func TestConfigCheckPreservesExactNames(t *testing.T) {
	write := configHome(t)
	write("config.toml", "[secrets]\nToken = 'printf one'\nTOKEN = 'printf two'\n")
	write("hosts.toml", "[[host]]\nname = 'Box'\nssh = 'one'\nlabels = { Name = 'one', NAME = 'two' }\n[[host]]\nname = 'box'\nssh = 'two'\n[[host]]\nName = 'managed'\n[host.MACHINE]\nPROVIDER = 'docker'\nID = 'one'\n")
	if r := checkConfig(); !r.OK || len(r.Findings) != 0 {
		t.Fatalf("exact names were folded: %+v", r)
	}
}

// Presence of a machine table exempts ssh only when that table identifies
// an actual provider and a non-blank machine id.
func TestConfigCheckMachineIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, table, message string
		lines                []int
	}{
		{"empty", "", "", []int{3, 3}},
		{"missing provider", "id = 'one'\n", "has no provider", []int{3}},
		{"empty provider", "provider = ''\nid = 'one'\n", "must be", []int{4}},
		{"blank provider", "provider = ' '\nid = 'one'\n", "must be", []int{4}},
		{"missing id", "provider = 'docker'\n", "needs a non-empty id", []int{3}},
		{"empty id", "provider = 'docker'\nid = ''\n", "needs a non-empty id", []int{5}},
		{"blank id", "provider = 'docker'\nID = '  '\n", "needs a non-empty id", []int{5}},
		{"wrong id type", "provider = 'docker'\nid = 7\n", "must be a string", []int{5}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			write := configHome(t)
			write("hosts.toml", "[[host]]\nname = 'box'\n[host.machine]\n"+tt.table)
			r := checkConfig()
			if r.OK || len(r.Findings) != len(tt.lines) {
				t.Fatalf("machine check = %+v, want %d findings", r, len(tt.lines))
			}
			for i, f := range r.Findings {
				if f.Line != tt.lines[i] || !strings.Contains(f.Message, tt.message) {
					t.Errorf("machine finding = %+v, want line %d and %q", f, tt.lines[i], tt.message)
				}
			}
		})
	}
}
