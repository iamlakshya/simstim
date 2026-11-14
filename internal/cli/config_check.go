package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/configdata"
)

// `config check` reads every config file, and reports each mistake with
// its file and line (docs/contributing/cli-spec.md 13).

func configCheckCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Find mistakes in the config files, each with its file and line",
		Long: "Reads config.toml and hosts.toml, and prints each mistake on one line, as file:line: message.\n" +
			"It finds TOML errors, unknown keys, and values of the wrong type or outside their list.\n" +
			"It checks legacy secret commands and structured definitions with the same rules as a session start.\n" +
			"It also finds two spellings of one key and machines without a provider or an id.\n" +
			"It also finds keys, sizes and times that do not parse, and hosts without a usable name or ssh.\n" +
			"For an unknown key, it names the nearest known key.\n" +
			"It exits with 1 when it finds an error, and with 0 when it finds only warnings, or nothing.\n" +
			"With --json, it prints the report as one object, with the same exit codes.",
		Example: "  " + brand.Name + " config check\n" +
			"  " + brand.Name + " config check --json",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			r := checkConfig()
			if asJSON {
				if err := printJSON(r); err != nil {
					return err
				}
			} else {
				r.write(os.Stdout)
			}
			// The findings are data: the report says them, and the exit
			// code says whether one is an error (cli-spec 7.4).
			if n := r.count(severityError); n > 0 {
				return &quietError{&client.UserError{Msg: "config check found " + countNoun(n, "error"),
					Hint: "Fix each error that the report lists, then run " + brand.Name + " config check again.", Code: "config_errors"}}
			}
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

// warnSettings warns, on the stderr of cmd, of a config.toml that does not
// decode or cannot be read, before the command goes on with the defaults
// (docs/contributing/cli-spec.md 13.4): two lines, with "Warning:" in
// yellow when stderr has colour, or one JSON warning line with --json.
// `config check` reports it itself, the launcher shows it in a notice, and
// the shell's completion of a word prints nothing.
func warnSettings(cmd *cobra.Command, args []string) {
	if policyWritesSettings(cmd) {
		return
	}
	launcher := !cmd.HasParent() || cmd.Name() == "flip" && len(args) == 0
	if launcher || cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd || shortPath(cmd) == "config check" {
		return
	}
	msg := client.SettingsWarning()
	if msg == "" {
		return
	}
	w, try := cmd.ErrOrStderr(), brand.Name+" config check"
	if jsonOn(cmd) {
		line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": map[string]string{"code": "config_ignored", "message": msg, "try": try}})
		fmt.Fprintf(w, "%s\n", jsonSafe(line))
		return
	}
	yellow, reset := "\x1b[33m", "\x1b[0m"
	if !colourOf(w) {
		yellow, reset = "", ""
	}
	fmt.Fprintf(w, "%sWarning:%s %s.\nTry: %s\n", yellow, reset, msg, try)
}

// The severities of a finding.
const (
	severityError   = "error"
	severityWarning = "warning"
)

// configReport is what `config check` found. Its JSON keys are those of
// docs/contributing/cli-spec.md 13.5.
type configReport struct {
	OK       bool          `json:"ok"`
	Files    []checkedFile `json:"files"`
	Findings []finding     `json:"findings"`
	// Layers names the policy sources for the current folder.
	Layers []client.LayerDescription `json:"layers"`
}

// checkedFile is one config file that `config check` looked for.
type checkedFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// finding is one mistake in a config file.
type finding struct {
	Path string `json:"path"`
	// Line is 0 when the key's line is not known.
	Line int `json:"line,omitempty"`
	// Column is set for a TOML error only.
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	// Message has no full stop at its end.
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// checkConfig checks each config file.
func checkConfig() configReport {
	r := configReport{Findings: []finding{}, Layers: []client.LayerDescription{}}
	for _, f := range client.ConfigFiles() {
		exists, found := checkFile(f)
		r.Files = append(r.Files, checkedFile{Path: tildeHome(f.Path), Exists: exists})
		r.Findings = append(r.Findings, found...)
	}
	r.addLayers()
	r.OK = r.count(severityError) == 0
	return r
}

// count is the number of findings of a severity.
func (r configReport) count(severity string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == severity {
			n++
		}
	}
	return n
}

// write prints the report: each finding in the GNU form file:line:
// message, a line for each file without one, and the totals.
func (r configReport) write(w io.Writer) {
	files := 0
	for _, f := range r.Files {
		n := 0
		for _, x := range r.Findings {
			if x.Path == f.Path {
				fmt.Fprintln(w, client.CleanLine(x.text()))
				n++
			}
		}
		switch {
		case !f.Exists:
			fmt.Fprintf(w, "%s: none yet\n", f.Path)
		case n == 0:
			fmt.Fprintf(w, "%s: no problems\n", f.Path)
		}
		if f.Exists {
			files++
		}
	}
	errs, warns := r.count(severityError), r.count(severityWarning)
	var found []string
	if errs > 0 {
		found = append(found, countNoun(errs, "error"))
	}
	if warns > 0 {
		found = append(found, countNoun(warns, "warning"))
	}
	switch {
	case files == 0:
		fmt.Fprintln(w, "No config files yet.")
	case len(found) == 0:
		fmt.Fprintf(w, "No problems in %s.\n", countNoun(files, "file"))
	default:
		fmt.Fprintf(w, "%s in %s.\n", strings.Join(found, " and "), countNoun(files, "file"))
	}
	writeConfigLayers(w, r.Layers)
}

// text is the finding on one line: `file:line: message`, with the column
// of a TOML error, "warning:" before a warning's message, and the
// suggestions after it.
func (f finding) text() string {
	where := f.Path
	if f.Line > 0 {
		where += ":" + strconv.Itoa(f.Line)
		if f.Column > 0 {
			where += ":" + strconv.Itoa(f.Column)
		}
	}
	msg := f.Message
	if f.Severity == severityWarning {
		msg = "warning: " + msg
	}
	if mean := didYouMean(f.Suggestions); mean != "" {
		return where + ": " + msg + ". " + mean
	}
	return where + ": " + strings.TrimSuffix(msg, ".") + "."
}

// plural is n and the word, with an s when n is not 1: "1 error", "2 files".
func countNoun(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// checkFile checks one config file, and reports whether it exists. Its
// time and its memory grow with the size of the file, which has a limit.
func checkFile(f client.ConfigFile) (bool, []finding) {
	b, err := client.ReadConfigFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	c := &checker{file: f.Name, path: tildeHome(f.Path), lines: scanTOML("")}
	switch {
	case errors.Is(err, client.ErrConfigTooLarge):
		c.add(nil, finding{Code: "too_large", Message: err.Error()})
		return true, c.out
	case err != nil:
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		c.add(nil, finding{Code: "unreadable", Message: "cannot read it: " + err.Error()})
		return true, c.out
	}
	c.lines = scanTOML(string(b))
	if c.lines.deep > 0 {
		c.add(nil, finding{Line: c.lines.deep, Code: "too_large", Message: c.lines.why})
		return true, c.out
	}
	var tree map[string]any
	if _, err := toml.Decode(string(b), &tree); err != nil {
		msg, line, col := client.TOMLWhere(string(b), err)
		c.add(nil, finding{Line: line, Column: col, Code: "toml_syntax", Message: strings.TrimSuffix(msg, ".")})
		return true, c.out
	}
	c.table(tree, f.Type, nil)
	// In the order of the file. A finding without a line comes last.
	sort.SliceStable(c.out, func(i, j int) bool {
		a, b := c.out[i].Line, c.out[j].Line
		return a != 0 && (b == 0 || a < b)
	})
	return true, c.out
}

// checker collects the findings of one file.
type checker struct {
	// file is the file's name, such as "config.toml", and path its path as
	// the report shows it.
	file, path string
	lines      *tomlLines
	out        []finding
}

// add records f, a finding about the key at p, or about the whole file
// when p is nil. An error is the default.
func (c *checker) add(p keyPath, f finding) {
	f.Path = c.path
	if f.Severity == "" {
		f.Severity = severityError
	}
	if p != nil {
		f.Line = c.lines.find(p)
	}
	c.out = append(c.out, f)
}

// table checks the keys of the table t, at p, against the struct typ,
// whose TOML tags are the keys that it knows. A key matches a tag as the
// TOML package matches it: exactly, or else in any case.
func (c *checker) table(t map[string]any, typ reflect.Type, p keyPath) {
	table := client.InspectConfigTable(t, typ)
	for _, key := range table.Keys {
		k, name, ft := key.Spelling, key.Name, key.Type
		if ft == nil {
			if c.layerUnknownKey(p, k) {
				continue
			}
			things := make([]named, 0, len(table.Fields))
			for n := range table.Fields {
				things = append(things, named{n, []string{n}})
			}
			c.add(p.add(k), finding{Code: "unknown_key", Message: "unknown key " + strconv.Quote(k) + in(p), Suggestions: suggest(k, things, false)})
			continue
		}
		if key.Conflict != "" {
			c.add(p.add(k), finding{Code: "bad_value", Message: key.Conflict + in(p)})
			continue
		}
		here := p.field(k, name)
		if !c.layerForbiddenKey(here) {
			c.value(t[k], ft, here)
		}
	}
	c.layerTable(t, typ, p)
}

// value checks v, the value of the key at p, against the Go type t, then
// runs the key's own check of valueChecks.
func (c *checker) value(v any, t reflect.Type, p keyPath) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[client.SecretDefinition]() {
		c.secretDefinition(v, p)
		return
	}
	_, table := v.(map[string]any)
	if err := configdata.CheckMapShape(t, table); err != nil {
		c.wrongType(p, v, t)
		return
	}
	var ok bool
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]any
		if m, ok = v.(map[string]any); ok {
			c.table(m, t, p)
		}
	case reflect.Slice:
		ok = c.array(v, t.Elem(), p)
	case reflect.Map:
		ok = c.configMap(v, t.Elem(), p)
	default:
		ok = isKind(v, t.Kind())
	}
	if !ok {
		c.wrongType(p, v, t)
		return
	}
	if check := valueChecks[c.file][p.name()]; check != nil {
		check(c, v, p)
	}
}

// array checks v, an array of elem.
func (c *checker) array(v any, elem reflect.Type, p keyPath) bool {
	if elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if elem.Kind() == reflect.Struct {
		ts, ok := tablesOf(v)
		for i, m := range ts {
			c.table(m, elem, p.index(i))
		}
		return ok
	}
	list, ok := v.([]any)
	for _, e := range list {
		ok = ok && isKind(e, elem.Kind())
	}
	return ok
}

// wrongType records a value v at p that is not a t.
func (c *checker) wrongType(p keyPath, v any, t reflect.Type) {
	c.add(p, finding{Code: "wrong_type", Message: fmt.Sprintf("%s must be %s, not %s", ref(p), wants(t), valueWords(v))})
}

// tablesOf is v as an array of tables, from [[host]] headers or inline.
func tablesOf(v any) ([]map[string]any, bool) {
	switch v := v.(type) {
	case []map[string]any:
		return v, true
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}
	return nil, false
}

// isKind reports whether v, as the TOML package decodes it, fits a Go
// value of kind k.
func isKind(v any, k reflect.Kind) bool {
	switch k {
	case reflect.String:
		_, ok := v.(string)
		return ok
	case reflect.Bool:
		_, ok := v.(bool)
		return ok
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		_, ok := v.(int64)
		return ok
	case reflect.Float32, reflect.Float64:
		_, isFloat := v.(float64)
		_, isInt := v.(int64)
		return isFloat || isInt
	case reflect.Interface:
		return true
	default:
		return false
	}
}
