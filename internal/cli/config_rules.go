package cli

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	tomlv2 "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keys"
	"github.com/iamlakshya/simstim/internal/tui"
)

type hostsCheckDoc struct {
	Hosts []client.Host `toml:"host"`
}

func checkSettingsFile(path string) (checkedFile, []configFinding) {
	abbrev := client.AbbrevPath(path)
	data, exists, err := client.ReadConfigFile(path)
	if !exists {
		return checkedFile{Path: abbrev, Exists: false}, nil
	}
	cf := checkedFile{Path: abbrev, Exists: true}
	if err != nil {
		return cf, []configFinding{readErrorFinding(abbrev, err)}
	}

	var s client.Settings
	strictErr, ins, err := client.DecodeConfigStrict(path, data, &s)
	if err != nil {
		return cf, []configFinding{decodeErrorFinding(abbrev, err)}
	}

	findings := make([]configFinding, 0, len(ins.Aliases))
	findings = append(findings, aliasFindings(abbrev, ins.Aliases)...)
	findings = append(findings, strictMissingFindings(abbrev, reflect.TypeOf(s), strictErr)...)
	findings = append(findings, validateSettings(abbrev, s, ins)...)
	return cf, findings
}

func checkHostsFile(path string) (checkedFile, []configFinding) {
	abbrev := client.AbbrevPath(path)
	data, exists, err := client.ReadConfigFile(path)
	if !exists {
		return checkedFile{Path: abbrev, Exists: false}, nil
	}
	cf := checkedFile{Path: abbrev, Exists: true}
	if err != nil {
		return cf, []configFinding{readErrorFinding(abbrev, err)}
	}

	var doc hostsCheckDoc
	strictErr, ins, err := client.DecodeConfigStrict(path, data, &doc)
	if err != nil {
		return cf, []configFinding{decodeErrorFinding(abbrev, err)}
	}

	findings := make([]configFinding, 0, len(ins.Aliases))
	findings = append(findings, aliasFindings(abbrev, ins.Aliases)...)
	findings = append(findings, strictMissingFindings(abbrev, reflect.TypeOf(doc), strictErr)...)
	findings = append(findings, validateHosts(abbrev, doc.Hosts, ins)...)
	return cf, findings
}

func readErrorFinding(abbrev string, err error) configFinding {
	var re *client.ConfigReadError
	if errors.As(err, &re) {
		return configFinding{
			Path:     abbrev,
			Line:     re.Line,
			Column:   re.Column,
			Severity: "error",
			Code:     re.Code,
			Message:  strings.TrimSuffix(re.Error(), "."),
		}
	}
	return configFinding{
		Path:     abbrev,
		Severity: "error",
		Code:     "unreadable",
		Message:  strings.TrimSuffix(err.Error(), "."),
	}
}

func decodeErrorFinding(abbrev string, err error) configFinding {
	var re *client.ConfigReadError
	if errors.As(err, &re) {
		return readErrorFinding(abbrev, err)
	}
	var de *tomlv2.DecodeError
	if errors.As(err, &de) {
		line, col := de.Position()
		msg := strings.TrimSuffix(strings.TrimPrefix(de.Error(), "toml: "), ".")
		code := "toml_syntax"
		if strings.Contains(msg, "cannot decode TOML") || strings.Contains(msg, "cannot store ") {
			code = "wrong_type"
			if k := de.Key(); len(k) > 0 {
				msg = fmt.Sprintf("key %q has the wrong type (%s)", strings.Join(k, "."), msg)
			}
		}
		return configFinding{
			Path:     abbrev,
			Line:     line,
			Column:   col,
			Severity: "error",
			Code:     code,
			Message:  msg,
		}
	}
	return configFinding{
		Path:     abbrev,
		Severity: "error",
		Code:     "toml_syntax",
		Message:  strings.TrimSuffix(err.Error(), "."),
	}
}

func aliasFindings(abbrev string, aliases []client.FieldAlias) []configFinding {
	out := make([]configFinding, 0, len(aliases))
	for _, a := range aliases {
		scope := "at the top level"
		if a.Table != "" {
			scope = "in [" + a.Table + "]"
		}
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     a.Pos.Line,
			Column:   a.Pos.Column,
			Severity: "error",
			Code:     "bad_value",
			Message:  fmt.Sprintf("%q and %q both name %q %s", a.First, a.Second, a.Canonical, scope),
		})
	}
	return out
}

func strictMissingFindings(abbrev string, rootType reflect.Type, strictErr *tomlv2.StrictMissingError) []configFinding {
	if strictErr == nil {
		return nil
	}
	var out []configFinding
	for _, e := range strictErr.Errors {
		line, col := e.Position()
		key := e.Key()
		if len(key) == 0 {
			continue
		}
		tablePath := key[:len(key)-1]
		leaf := key[len(key)-1]
		known := client.KnownStructKeys(rootType, tablePath)
		sugg := suggestFromStrings(leaf, known)
		msg := fmt.Sprintf("unknown key %q", leaf)
		if len(tablePath) > 0 {
			msg = fmt.Sprintf("unknown key %q in [%s]", leaf, strings.Join(tablePath, "."))
		}
		out = append(out, configFinding{
			Path:        abbrev,
			Line:        line,
			Column:      col,
			Severity:    "error",
			Code:        "unknown_key",
			Message:     msg,
			Suggestions: sugg,
		})
	}
	return out
}

func suggestFromStrings(typed string, known []string) []string {
	if len(known) == 0 {
		return nil
	}
	things := make([]named, len(known))
	for i, k := range known {
		things[i] = named{name: k, answers: []string{k}}
	}
	return suggest(typed, things, false)
}

func validateSettings(abbrev string, s client.Settings, ins *client.TOMLInspection) []configFinding {
	var out []configFinding
	addBad := func(pos client.ConfigPos, msg string, sugg []string) {
		out = append(out, configFinding{
			Path:        abbrev,
			Line:        pos.Line,
			Column:      pos.Column,
			Severity:    "error",
			Code:        "bad_value",
			Message:     msg,
			Suggestions: sugg,
		})
	}

	if v := s.UI.Motion; v != "" && v != "auto" && v != "on" && v != "off" {
		addBad(ins.Pos("ui", "motion"),
			fmt.Sprintf(`ui.motion must be "auto", "on" or "off", not %q`, v),
			suggestFromStrings(v, []string{"auto", "on", "off"}))
	}
	if v := s.Attach.Scrollback; v != "" && v != "keep" && v != "clear" {
		addBad(ins.Pos("attach", "scrollback"),
			fmt.Sprintf(`attach.scrollback must be "keep" or "clear", not %q`, v),
			suggestFromStrings(v, []string{"keep", "clear"}))
	}
	if v := s.Attach.DetachKey; v != "" {
		if _, err := keys.Parse(v); err != nil {
			addBad(ins.Pos("attach", "detach_key"), "attach.detach_key: "+err.Error(), nil)
		}
	}
	if v := s.Attach.ImagePasteKey; v != "" {
		if _, err := keys.Parse(v); err != nil {
			addBad(ins.Pos("attach", "image_paste_key"), "attach.image_paste_key: "+err.Error(), nil)
		}
	}
	if v := s.Sessions.MemoryMax; v != "" {
		if _, err := api.ParseMemoryMax(v); err != nil {
			addBad(ins.Pos("sessions", "memory_max"), "sessions.memory_max: "+err.Error(), nil)
		}
	}
	out = append(out, validatePackageSettings(abbrev, s, ins)...)
	out = append(out, validateKeymap(abbrev, s.Keymap, ins)...)
	out = append(out, validateSecrets(abbrev, s.Secrets, ins)...)
	return out
}

func validateKeymap(abbrev string, km map[string]any, ins *client.TOMLInspection) []configFinding {
	if len(km) == 0 {
		return nil
	}
	knownCmds := tui.KeymapCommands()
	knownSet := make(map[string]bool, len(knownCmds))
	for _, c := range knownCmds {
		knownSet[c] = true
	}
	oldMap := oldKeymapForms()

	names := make([]string, 0, len(km))
	for k := range km {
		names = append(names, k)
	}
	sort.Strings(names)

	var out []configFinding
	for _, name := range names {
		pos := ins.Pos("keymap", name)
		if replacement, isOld := oldMap[name]; isOld {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "warning",
				Code:     "old_name",
				Message:  fmt.Sprintf("%q is an old name; use %q", name, replacement),
			})
		} else if !knownSet[name] {
			out = append(out, configFinding{
				Path:        abbrev,
				Line:        pos.Line,
				Column:      pos.Column,
				Severity:    "error",
				Code:        "bad_value",
				Message:     fmt.Sprintf("unknown launcher command %q in [keymap]", name),
				Suggestions: suggestFromStrings(name, knownCmds),
			})
		}
		out = append(out, validateKeymapValue(abbrev, name, km[name], pos)...)
	}
	return out
}

func oldKeymapForms() map[string]string {
	out := map[string]string{}
	for _, o := range oldForms {
		if o.Kind == "keymap name" {
			out[o.Old] = o.New
		}
	}
	return out
}

func validateKeymapValue(abbrev, name string, val any, pos client.ConfigPos) []configFinding {
	var out []configFinding
	checkOne := func(k string) {
		if err := tui.CheckKey(k); err != nil {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  fmt.Sprintf("[keymap] %q: %s", name, err.Error()),
			})
		}
	}
	switch x := val.(type) {
	case string:
		checkOne(x)
	case bool:
		// false unbinds; true keeps defaults.
	case []any:
		for _, elem := range x {
			s, ok := elem.(string)
			if !ok {
				out = append(out, configFinding{
					Path:     abbrev,
					Line:     pos.Line,
					Column:   pos.Column,
					Severity: "error",
					Code:     "wrong_type",
					Message:  fmt.Sprintf("[keymap] %q list entries must be key strings", name),
				})
				continue
			}
			checkOne(s)
		}
	default:
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     "wrong_type",
			Message:  fmt.Sprintf("[keymap] %q must be a key string, a list of key strings, or a boolean", name),
		})
	}
	return out
}

func validateSecrets(abbrev string, secrets map[string]string, ins *client.TOMLInspection) []configFinding {
	if len(secrets) == 0 {
		return nil
	}
	names := make([]string, 0, len(secrets))
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)

	var out []configFinding
	for _, name := range names {
		pos := ins.Pos("secrets", name)
		if !client.ValidSecretName(name) {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  fmt.Sprintf("%q cannot name a secret in [secrets]", name),
			})
		}
		if strings.TrimSpace(secrets[name]) == "" {
			out = append(out, configFinding{
				Path:     abbrev,
				Line:     pos.Line,
				Column:   pos.Column,
				Severity: "error",
				Code:     "bad_value",
				Message:  fmt.Sprintf("secret %q in [secrets] has an empty command", name),
			})
		}
	}
	return out
}

func validateHosts(abbrev string, hosts []client.Host, ins *client.TOMLInspection) []configFinding {
	out := make([]configFinding, 0, len(hosts))
	seenNames := map[string]bool{}
	for i, h := range hosts {
		out = append(out, validateHostEntry(abbrev, i, h, seenNames, ins)...)
	}
	return out
}

func validateHostEntry(abbrev string, idx int, h client.Host, seenNames map[string]bool, ins *client.TOMLInspection) []configFinding {
	var out []configFinding
	addPos := func(pos client.ConfigPos, code, msg string) {
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     code,
			Message:  msg,
		})
	}

	namePos := ins.HostPos(idx, "name")
	switch {
	case strings.TrimSpace(h.Name) == "":
		addPos(ins.HostPos(idx), "bad_host", "host entry is missing a name")
	case !client.ValidHostName(h.Name):
		addPos(namePos, "bad_host", fmt.Sprintf("%q cannot name a host", h.Name))
	case seenNames[h.Name]:
		addPos(namePos, "bad_host", fmt.Sprintf("duplicate host name %q", h.Name))
	default:
		seenNames[h.Name] = true
	}

	if h.Machine == nil && strings.TrimSpace(h.SSH) == "" {
		addPos(ins.HostPos(idx, "ssh"), "bad_host", fmt.Sprintf("host %q needs ssh", hostLabel(h, idx)))
	}
	if h.Provider != nil && strings.TrimSpace(h.Provider.Script) == "" {
		addPos(ins.HostPos(idx, "provider", "script"), "bad_value",
			fmt.Sprintf("host %q has [host.provider] without a script", hostLabel(h, idx)))
	}
	if h.Machine != nil {
		out = append(out, validateMachine(abbrev, idx, h, ins)...)
	}
	for _, sec := range h.Secrets {
		if !client.ValidSecretName(sec) {
			addPos(ins.HostPos(idx, "secrets"), "bad_value",
				fmt.Sprintf("host %q lists invalid secret name %q", hostLabel(h, idx), sec))
		}
	}
	if v := h.WallFloor; v != "" && v != "none" && v != "process" && v != "gvisor" && v != "microvm" {
		pos := ins.HostPos(idx, "wall_floor")
		out = append(out, configFinding{
			Path:        abbrev,
			Line:        pos.Line,
			Column:      pos.Column,
			Severity:    "error",
			Code:        "bad_value",
			Message:     fmt.Sprintf(`host %q wall_floor must be "none", "process", "gvisor" or "microvm", not %q`, hostLabel(h, idx), v),
			Suggestions: suggestFromStrings(v, []string{"none", "process", "gvisor", "microvm"}),
		})
	}
	return out
}

func validateMachine(abbrev string, idx int, h client.Host, ins *client.TOMLInspection) []configFinding {
	var out []configFinding
	addPos := func(pos client.ConfigPos, msg string) {
		out = append(out, configFinding{
			Path:     abbrev,
			Line:     pos.Line,
			Column:   pos.Column,
			Severity: "error",
			Code:     "bad_value",
			Message:  msg,
		})
	}
	m := h.Machine
	label := hostLabel(h, idx)
	if strings.TrimSpace(m.Provider) == "" {
		addPos(ins.HostPos(idx, "machine", "provider"), fmt.Sprintf("host %q has [host.machine] without a provider", label))
	}
	if strings.TrimSpace(m.ID) == "" {
		addPos(ins.HostPos(idx, "machine", "id"), fmt.Sprintf("host %q has [host.machine] without an id", label))
	}
	if err := client.CheckIdleStop(m.IdleStop); err != nil {
		addPos(ins.HostPos(idx, "machine", "idle_stop"),
			fmt.Sprintf("host %q idle_stop: %s", label, strings.TrimSuffix(err.Error(), ".")))
	}
	if v := m.BusyLease; v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			addPos(ins.HostPos(idx, "machine", "busy_lease"),
				fmt.Sprintf("host %q busy_lease: %q is not a positive duration", label, v))
		}
	}
	if v := m.Stdin; v != "" && v != "send" && v != "stream" {
		out = append(out, configFinding{
			Path:        abbrev,
			Line:        ins.HostPos(idx, "machine", "stdin").Line,
			Column:      ins.HostPos(idx, "machine", "stdin").Column,
			Severity:    "error",
			Code:        "bad_value",
			Message:     fmt.Sprintf(`host %q stdin must be "send" or "stream", not %q`, label, v),
			Suggestions: suggestFromStrings(v, []string{"send", "stream"}),
		})
	}
	return out
}

func hostLabel(h client.Host, idx int) string {
	if strings.TrimSpace(h.Name) != "" {
		return h.Name
	}
	return fmt.Sprintf("#%d", idx+1)
}
