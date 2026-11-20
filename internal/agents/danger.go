package agents

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Span is a run of arguments in a command: N arguments from index I.
type Span struct{ I, N int }

// Dangers returns each run of argv that turns the agent's own approvals
// off, in order. A run is a flag of DangerFlags, with the value after it
// when the rule names values, or one argument that holds one of DangerText.
func (a Agent) Dangers(argv []string) []Span {
	var out []Span
	for i := range argv {
		if n, ok := a.dangerFlag(argv, i); ok {
			out = append(out, Span{I: i, N: n})
			continue
		}
		if a.dangerText(argv[i]) {
			out = append(out, Span{I: i, N: 1})
		}
	}
	return out
}

// splitRule splits a rule of DangerFlags or DangerText into its name and
// the values that it names. "name!=a|b" names the values that are safe
// (except is true), "name=a|b" the values that are dangerous, and "name"
// no values.
func splitRule(rule string) (name string, values []string, except bool) {
	if k, v, ok := strings.Cut(rule, "!="); ok {
		return k, strings.Split(v, "|"), true
	}
	if k, v, ok := strings.Cut(rule, "="); ok {
		return k, strings.Split(v, "|"), false
	}
	return rule, nil, false
}

// dangerous reports whether value makes a rule with these values match.
// A safe value must be the same exactly. A dangerous one may differ in
// case. "*" is any value, and "{}" is a JSON object.
func dangerous(value string, values []string, except bool) bool {
	in := slices.ContainsFunc(values, func(v string) bool {
		switch {
		case v == "*":
			return true
		case v == "{}":
			_, ok := jsonObject(value)
			return ok
		case except:
			return value == v
		}
		return strings.EqualFold(value, v)
	})
	return in != except
}

// listValues is the value part of a DangerFlags rule for a flag that takes
// a list of values, such as Claude Code's --allowedTools a b c.
const listValues = "..."

// dangerFlag reports whether argv[i] is one of DangerFlags, and how many
// arguments the match takes: the flag, and the values that it consumes.
// The run holds those values, so a value that the user typed allows only
// that value. A rule's arity follows its form: "--flag" is a switch,
// "--flag=..." takes a list up to the next argument that starts with "-",
// and any other "=" or "!=" rule takes one value.
func (a Agent) dangerFlag(argv []string, i int) (int, bool) {
	arg := argv[i]
	for _, rule := range a.DangerFlags {
		flag, values, except := splitRule(rule)
		// A short flag (-p) takes its value with no "=" too, as in -pwork. A
		// long flag (--yolo) must not match a longer one (--yolo-mode).
		short := len(flag) == 2 && flag[0] == '-' && flag[1] != '-'
		var value string
		joined := true
		switch {
		case arg == flag:
			joined = false
		case strings.HasPrefix(arg, flag+"="):
			value = arg[len(flag)+1:]
		case short && strings.HasPrefix(arg, flag):
			value = arg[len(flag):]
		default:
			continue
		}
		n := 1
		switch {
		case values == nil:
			return n, true
		case len(values) == 1 && values[0] == listValues:
			// A list takes each value up to the next flag, joined or not:
			// counting more values only makes the run longer, and so the
			// check stricter.
			for i+n < len(argv) && !strings.HasPrefix(argv[i+n], "-") {
				n++
			}
			return n, true
		case !joined && i+1 < len(argv):
			value, n = argv[i+1], 2
		}
		if dangerous(value, values, except) {
			return n, true
		}
	}
	return 0, false
}

// dangerText reports whether arg holds one of DangerText, as written or in
// what it decodes to.
func (a Agent) dangerText(arg string) bool {
	if len(a.DangerText) == 0 {
		return false
	}
	d := decode(arg)
	for _, rule := range a.DangerText {
		key, values, except := splitRule(rule)
		if values == nil {
			if d.holds(rule) {
				return true
			}
		} else if d.hasKey(key, values, except) {
			return true
		}
	}
	return false
}

// decoded is what an agent may read from one argument: the argument as
// written, and the keys and values of a JSON object or of a key=value
// override that it holds.
type decoded struct {
	// texts are the argument, and each key path and string it decodes to.
	texts []string
	// pairs are each key path, such as "permissions.defaultMode", and its
	// value: a string as it decodes, or the value's text for any other.
	pairs [][2]string
}

// decode reads arg as the agents do. Claude Code reads a --settings or
// --agents value as JSON. Codex reads a -c override as a key as written,
// and a value in TOML, or as a plain string when the TOML does not parse
// (parse_toml_value in codex-rs/utils/cli/src/config_override.rs).
func decode(arg string) decoded {
	d := decoded{texts: []string{arg}}
	d.add(arg)
	return d
}

// add decodes s: a JSON object, or a key=value override. In --flag=value,
// it decodes the value.
func (d *decoded) add(s string) {
	if obj, ok := jsonObject(s); ok {
		d.walk("", obj)
		return
	}
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return
	}
	k = strings.TrimSpace(k)
	switch {
	case strings.HasPrefix(k, "--"):
		d.add(v)
	case strings.HasPrefix(k, "-"):
		// A short flag with its value joined, as in -cprofile=work.
		d.add(v)
		if len(k) > 2 {
			d.walk(k[2:], tomlValue(v))
		}
	case k != "":
		d.walk(k, tomlValue(v))
	}
}

// walk adds the key path path with its value v, and every key and string
// inside v.
func (d *decoded) walk(path string, v any) {
	if path != "" {
		d.texts = append(d.texts, path)
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprint(v)
		}
		d.pairs = append(d.pairs, [2]string{path, s})
	}
	switch v := v.(type) {
	case map[string]any:
		for k, c := range v {
			if path != "" {
				k = path + "." + k
			}
			d.walk(k, c)
		}
	case []any:
		for _, c := range v {
			d.walk(path, c)
		}
	case []map[string]any:
		for _, c := range v {
			d.walk(path, c)
		}
	case string:
		d.texts = append(d.texts, v)
	}
}

// holds reports whether any text holds t, in any case.
func (d decoded) holds(t string) bool {
	t = strings.ToLower(t)
	return t != "" && slices.ContainsFunc(d.texts, func(s string) bool { return strings.Contains(strings.ToLower(s), t) })
}

// hasKey reports whether a key path, or its last part, is key, in any
// case, with a value that makes the rule match.
func (d decoded) hasKey(key string, values []string, except bool) bool {
	return slices.ContainsFunc(d.pairs, func(p [2]string) bool {
		last := p[0][strings.LastIndex(p[0], ".")+1:]
		return (strings.EqualFold(p[0], key) || strings.EqualFold(last, key)) && dangerous(p[1], values, except)
	})
}

// jsonObject decodes s when it is a JSON object.
func jsonObject(s string) (map[string]any, bool) {
	if !strings.HasPrefix(strings.TrimSpace(s), "{") {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil, false
	}
	return m, true
}

// tomlValue decodes the value of a key=value override as Codex does: as
// TOML, or else as the text without its quotes.
func tomlValue(s string) any {
	s = strings.TrimSpace(s)
	var m map[string]any
	if _, err := toml.Decode("_x_ = "+s, &m); err == nil {
		if v, ok := m["_x_"]; ok {
			return v
		}
	}
	return strings.Trim(s, `"'`)
}
