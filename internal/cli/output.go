package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/client"
)

// Machine output and the streams (docs/contributing/cli-spec.md 7). Data
// goes to stdout, and everything else to stderr: messages, confirmations,
// progress, warnings, prompts and errors. With --json, stdout gets one JSON
// value, and stderr gets only JSON lines.

// jsonVersion is the value of "v" in every top-level object.
const jsonVersion = 1

// addJSON gives cmd the flag --json, one of the flags of cli-spec 10.2
// that mean one thing everywhere (flags.go).
func addJSON(cmd *cobra.Command, p *bool) {
	cmd.Flags().BoolVar(p, "json", false, "print JSON for scripts, and ask nothing (see docs/how-to/scripting.md)")
	shared(cmd.Flags(), "json")
}

// jsonOn reports whether cmd prints JSON.
func jsonOn(cmd *cobra.Command) bool { return boolFlag(cmd, "json") }

// addRootFlags gives every command --no-input and --no-color.
func addRootFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.Bool("no-input", false, "ask nothing: a question that needs an answer fails with exit code 4, and names the flag that answers it ("+noInputEnv+"=1 does the same)")
	f.Bool("no-color", false, "print no colour (NO_COLOR does the same)")
	shared(f, "no-input")
	shared(f, "no-color")
}

// applyNoColor makes --no-color hold for everything this run draws,
// the launcher too: it sets NO_COLOR, which they all read.
func applyNoColor(cmd *cobra.Command) {
	if boolFlag(cmd, "no-color") {
		_ = os.Setenv("NO_COLOR", "1")
	}
}

// printf prints data on stdout: a list, details or a report.
func printf(format string, a ...any) { fmt.Printf(format, a...) }

// say prints a message on stderr: a confirmation, a note or progress. With
// --json it prints nothing, as stderr then gets only JSON lines.
func say(cmd *cobra.Command, format string, a ...any) {
	if cmd != nil && jsonOn(cmd) {
		return
	}
	fmt.Fprintf(os.Stderr, format, a...)
}

// note prints a note that a script may need, such as a step that the
// command skipped, on stderr: one line, or with --json one JSON line, as a
// warning with code. The message may hold a host's error, so it prints
// cleaned.
func note(cmd *cobra.Command, code, msg string) {
	if cmd != nil && jsonOn(cmd) {
		line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": map[string]string{"code": code, "message": msg}})
		fmt.Fprintf(os.Stderr, "%s\n", jsonSafe(line))
		return
	}
	fmt.Fprintln(os.Stderr, shownNote(msg))
}

// warningFor returns a client.WarnFunc that writes client warnings (such
// as a changed host ID) to stderr: as Warning:/Try: lines, or one JSON
// warning line when asJSON is true.
func warningFor(asJSON bool) client.WarnFunc {
	return func(code, msg, hint string) {
		msg = shown(msg)
		hint = shown(hint)
		if asJSON {
			w := map[string]string{"code": code, "message": msg}
			if hint != "" {
				w["try"] = hint
			}
			line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": w})
			fmt.Fprintf(os.Stderr, "%s\n", jsonSafe(line))
			return
		}
		fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		if hint != "" {
			fmt.Fprintf(os.Stderr, "Try: %s\n", hint)
		}
	}
}

// stderrFor is stderr for messages that a part of the client writes by
// itself, such as the debug file's warning: stderr as it is, or with JSON
// a writer that turns each message into one warning line with code.
func stderrFor(asJSON bool, code string) io.Writer {
	if !asJSON {
		return os.Stderr
	}
	return jsonWarnings{w: os.Stderr, code: code}
}

// jsonWarnings writes each message it gets as one JSON warning line.
type jsonWarnings struct {
	w    io.Writer
	code string
}

func (j jsonWarnings) Write(b []byte) (int, error) {
	msg := strings.Join(strings.Fields(string(b)), " ")
	line, _ := json.Marshal(map[string]any{"v": jsonVersion, "warning": map[string]string{"code": j.code, "message": msg}})
	if _, err := fmt.Fprintf(j.w, "%s\n", jsonSafe(line)); err != nil {
		return 0, fmt.Errorf("write a warning: %w", err)
	}
	return len(b), nil
}

// printJSON prints v on stdout for scripts: one value, indented by two
// spaces, with one newline at its end. A top-level object, and each object
// of a top-level array, gets "v": 1 (cli-spec 7.2).
func printJSON(v any) error {
	b, err := versioned(v)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(jsonSafe(b)); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

// versioned is v as printJSON prints it.
func versioned(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("write JSON: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	switch {
	case bytes.HasPrefix(raw, []byte("[")):
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("write JSON: %w", err)
		}
		if items == nil {
			items = []json.RawMessage{}
		}
		for i, it := range items {
			items[i] = withV(it)
		}
		if raw, err = json.Marshal(items); err != nil {
			return nil, fmt.Errorf("write JSON: %w", err)
		}
	default:
		raw = withV(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("write JSON: %w", err)
	}
	out.WriteByte('\n')
	return jsonSafe(out.Bytes()), nil
}

// withV puts "v": 1 first in an object. Any other value stays as it is.
func withV(obj json.RawMessage) json.RawMessage {
	obj = bytes.TrimSpace(obj)
	if !bytes.HasPrefix(obj, []byte("{")) {
		return obj
	}
	rest := bytes.TrimSpace(obj[1:])
	v := fmt.Sprintf(`{"v":%d`, jsonVersion)
	if bytes.Equal(rest, []byte("}")) {
		return json.RawMessage(v + "}")
	}
	return json.RawMessage(v + "," + string(rest))
}

// sessionObject is a session for scripts (cli-spec 7.1): its host, its
// address, and every field that has a value. The address is always
// host/ID, never a name: a name can change, or come to mean another
// session, between the list that it came from and the script's next step.
func sessionObject(host string, s client.Session) map[string]any {
	obj := valuesOf(s)
	obj["host"] = host
	obj["address"] = host + "/" + s.ID
	return obj
}

// valuesOf is v as a JSON object, without the keys that hold no value: an
// empty string, a zero time or null (cli-spec 7.2, rule 6).
func valuesOf(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	obj := make(map[string]any, len(values))
	for key, value := range values {
		switch string(bytes.TrimSpace(value)) {
		case "null", `""`, `"0001-01-01T00:00:00Z"`:
			continue
		}
		// Preserve encoded integers and nested data without a float64 pass.
		obj[key] = value
	}
	return obj
}

// hostObject is a saved host as `host ls --json` prints it: its keys in
// snake_case, as hosts.toml names them, its tier from tiers.toml, and,
// until the end of the old form, the same keys in Go form too
// (cli-spec 3.2, question C-9).
func hostObject(h client.Host, tier client.Tier) map[string]any {
	obj := map[string]any{}
	raw, _ := json.Marshal(h)
	var old map[string]json.RawMessage
	_ = json.Unmarshal(raw, &old)
	for k, v := range old {
		obj[k] = v
	}
	if m, ok := tomlKeys(reflect.ValueOf(h)).(map[string]any); ok {
		for k, v := range m {
			obj[k] = v
		}
	}
	if tier.Valid() {
		obj["tier"] = tier.String()
	}
	return obj
}

// tomlKeys is v with the keys of its TOML tags. A field tagged omitempty,
// a nil pointer and an empty value are left out.
func tomlKeys(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return tomlKeys(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := range t.NumField() {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ",")
			if name == "" || name == "-" || !t.Field(i).IsExported() {
				continue
			}
			f := v.Field(i)
			if f.IsZero() || (f.Kind() == reflect.Slice || f.Kind() == reflect.Map) && f.Len() == 0 {
				continue
			}
			out[name] = tomlKeys(f)
		}
		return out
	default:
		return v.Interface()
	}
}

// errorJSON is an error as one JSON line on stderr (cli-spec 7.4).
type errorJSON struct {
	Code        string          `json:"code"`
	Kind        string          `json:"kind"`
	Message     string          `json:"message"`
	Try         string          `json:"try,omitempty"`
	Exit        int             `json:"exit"`
	Suggestions []string        `json:"suggestions,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// printJSON writes the report as one JSON line on w, and returns its exit
// code. Any text of the report may be a host's, or hold a host's: its code,
// its message, its Try: line, its suggestions and its data. Each prints
// encoded with unsafe runes escaped, keeping the exact decoded values.
func (r report) printJSON(w io.Writer) int {
	code := exitCode(r.kind)
	line, _ := json.Marshal(struct {
		V     int       `json:"v"`
		Error errorJSON `json:"error"`
	}{jsonVersion, errorJSON{Code: r.code, Kind: r.kind.String(), Message: r.msg, Try: r.try, Exit: code, Suggestions: r.suggestions, Data: validJSONData(r.data)}})
	fmt.Fprintf(w, "%s\n", jsonSafe(line))
	return code
}

// jsonAsked reports whether the command line asks for JSON. When the
// flags of the command that ran parsed, it is their --json, so a last
// --json=false counts. When they did not, the line is read as the parser
// reads it, wherever it stopped (flagOn).
func jsonAsked(cmd *cobra.Command, args []string) bool {
	if cmd != nil && cmd.Annotations[parseFailed] == "" {
		return jsonOn(cmd)
	}
	return flagOn(cmd, args, "json")
}

// colourAsked reports whether the command line leaves colour on: neither
// --no-color, which the run may not have read when the line did not parse
// (flagOn), nor NO_COLOR nor TERM=dumb.
func colourAsked(cmd *cobra.Command, args []string, f *os.File) bool {
	return !flagOn(cmd, args, "no-color") && colourOn(f)
}

// flagOn reports whether args turn the bool flag name on, as the flags of
// cmd read the line, and as pflag does: before any "--", a word that is
// another flag's value, such as --json in "--debug-file --json", is no
// flag, --name=<bool> counts by its value, and the last one wins. Without
// cmd, the flags of the top command read it.
func flagOn(cmd *cobra.Command, args []string, name string) bool {
	if cmd == nil {
		// Reading global flags must not build executable command handlers.
		cmd = &cobra.Command{}
		addRootFlags(cmd)
		addDebugFlags(cmd)
		return boolFlagAsked(cmd.PersistentFlags(), args, name)
	}
	return boolFlagAsked(cmd.Flags(), args, name)
}

func boolFlagAsked(fs *pflag.FlagSet, args []string, name string) bool {
	on := false
	for _, w := range words(fs, args) {
		if !w.isFlag {
			continue
		}
		a := args[w.at]
		if a == "--"+name {
			on = true
		} else if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				on = b
			}
		}
	}
	return on
}

// beforeAttach is the check of new and resume before any lookup and any
// remote work: --json needs --detach, and an attach needs a terminal
// (cli-spec 9.3, rule 2). So a script without a terminal opens no
// connection, and starts or resumes no session.
func beforeAttach(cmd *cobra.Command, asJSON, detach bool) error {
	return beforeAttachFiles(cmd, asJSON, detach, os.Stdin, os.Stdout)
}

func beforeAttachFiles(cmd *cobra.Command, asJSON, detach bool, in, out *os.File) error {
	switch {
	case detach:
		return nil
	case asJSON:
		return needsDetach(cmd)
	}
	return attachNeedsTerminalFiles(cmd, in, out)
}

// attachNeedsATerminal is the error of new or resume without --detach when
// stdin or stdout is not a terminal, or nil. Its Try: line puts --detach
// before any "--".
func attachNeedsATerminal(cmd *cobra.Command) error {
	return attachNeedsTerminalFiles(cmd, os.Stdin, os.Stdout)
}

func attachNeedsTerminalFiles(cmd *cobra.Command, in, out *os.File) error {
	if isTerminal(in) && isTerminal(out) {
		return nil
	}
	return &client.UserError{
		Msg:  shortPath(cmd) + " attaches, so it needs a terminal",
		Hint: prompts(cmd).withFlags("--detach"),
		Code: "no_terminal",
		Kind: client.KindUsage,
	}
}

// needsDetach is the error of --json on new or resume without --detach.
// Its Try: line puts --detach before any "--".
func needsDetach(cmd *cobra.Command) error {
	return &client.UserError{
		Msg:  "--json needs --detach, because an attached session owns the terminal",
		Hint: prompts(cmd).withFlags("--detach"),
		Code: "json_needs_detach",
		Kind: client.KindUsage,
	}
}
