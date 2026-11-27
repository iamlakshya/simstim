package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// configHome points the config folder at ~/.config/<name> in a new home,
// and returns a function that writes one of its files.
func configHome(t *testing.T) func(name, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", brand.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The acceptance line of task C9: a typo in a key's name gets the nearest
// key, and config check exits with 1, with nothing on stderr.
func TestConfigCheckTypo(t *testing.T) {
	write := configHome(t)
	write("config.toml", "# my settings\n[ui]\n# the launcher's animation\nmotoin = \"on\"\n")
	out, err := run(t, "config", "check")
	want := "~/.config/" + brand.Name + "/config.toml:4: unknown key \"motoin\" in [ui]. Did you mean \"motion\"?\n" +
		"~/.config/" + brand.Name + "/hosts.toml: none yet\n" +
		"1 error in 1 file.\n"
	if out != want {
		t.Errorf("config check printed:\n%s\nwant:\n%s", out, want)
	}
	var b bytes.Buffer
	if code := finish(&b, err, nil, false, false, false); code != 1 || b.Len() != 0 {
		t.Errorf("config check exits with %d, and prints %q on stderr", code, b.String())
	}
}

// A clean file, and no files at all, exit with 0.
func TestConfigCheckClean(t *testing.T) {
	write := configHome(t)
	if out := ok(t, "config", "check"); !strings.Contains(out, "hosts.toml: none yet\nNo config files yet.\n") || !strings.Contains(out, "\nLayers for ") {
		t.Errorf("with no files, config check printed:\n%s", out)
	}
	write("config.toml", "[ui]\nmotion = \"off\"\n[keymap]\n\"session: delete\" = \"x\"\n\"host: add\" = false\n[secrets]\nTOKEN = \"op read op://dev/token\"\n")
	write("hosts.toml", "[[host]]\nname = \"box\"\nssh = \"box\"\nlabels = { provider = \"gcp\" }\n[host.machine]\nprovider = \"lima\"\nid = \"box\"\nidle_stop = \"never\"\n")
	out := ok(t, "config", "check")
	if !strings.Contains(out, "config.toml: no problems\n") || !strings.Contains(out, "hosts.toml: no problems\nNo problems in 2 files.\n") || !strings.Contains(out, "\nLayers for ") {
		t.Errorf("clean files give:\n%s", out)
	}
	// A usage error of config check itself exits with 2.
	if got, code := lineErr(t, "config", "check", "extra"); code != 2 || !strings.Contains(got, "config check takes no words") {
		t.Errorf("config check extra = %d:\n%s", code, got)
	}
}

// One broken file for each row of the checks, with the line of each
// finding (docs/contributing/cli-spec.md 13.2 and 13.3).
func TestConfigCheckFindings(t *testing.T) {
	for _, c := range []struct {
		name, file, body string
		line             int
		code, has        string
	}{
		{"syntax", "config.toml", "[ui]\nmotion = \n", 2, "toml_syntax", "expected value"},
		{"syntax in hosts", "hosts.toml", "[[host]]\nname = = \"box\"\n", 2, "toml_syntax", "expected value"},
		{"unknown key", "config.toml", "[ui]\nmotoin = \"on\"\n", 2, "unknown_key", `unknown key "motoin" in [ui]`},
		{"unknown table", "config.toml", "\n[iu]\nmotion = \"on\"\n", 2, "unknown_key", `unknown key "iu"`},
		{"unknown host key", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\n[[host]]\nname = \"b\"\nshh = \"b\"\nssh = \"b\"\n", 6, "unknown_key", `unknown key "shh" in [[host]]`},
		{"wrong type", "config.toml", "[ui]\n\nmotion = 5\n", 3, "wrong_type", `"motion" in [ui] must be a string, not 5`},
		{"wrong type of a table", "config.toml", "ui = \"on\"\n", 1, "wrong_type", `"ui" must be a table, not "on"`},
		{"wrong type in a list", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\nsecrets = [\"A\", 1]\n", 4, "wrong_type", "must be an array of strings"},
		{"wrong type of a label", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\n[host.labels]\nzone = 1\n", 5, "wrong_type", `"zone" in [host.labels] must be a string, not 1`},
		{"motion", "config.toml", "[ui]\nmotion = \"fast\"\n", 2, "bad_value", `must be "auto", "on" or "off", not "fast"`},
		{"scrollback", "config.toml", "[attach]\nscrollback = \"kept\"\n", 2, "bad_value", `must be "keep" or "clear"`},
		{"size", "config.toml", "[sessions]\nmemory_max = \"4X\"\n", 2, "bad_value", `"memory_max" in [sessions]: "4X" is not a size. Use auto`},
		{"idle time", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\n[host.machine]\nprovider = \"lima\"\nidle_stop = \"5x\"\nid = \"a\"\n", 6, "bad_value", "must be a time"},
		{"idle time under a minute", "hosts.toml", "[[host]]\nname = \"a\"\n[host.machine]\nprovider = \"lima\"\nidle_stop = \"59s\"\nid = \"a\"\n", 5, "bad_value", "must be a time of a minute or more"},
		{"a key in another case", "config.toml", "[UI]\nMotion = \"fast\"\n", 2, "bad_value", `"Motion" in [UI] must be "auto"`},
		{"a key in an inline table over lines", "hosts.toml", "[[host]]\nname = \"a\"\nmachine = {\n  provider = \"lima\",\n  id = \"a\",\n  usr = \"me\",\n}\n", 6, "unknown_key", `unknown key "usr" in [host.machine]`},
		{"a key of an inline table in an array", "hosts.toml", "host = [\n  { name = \"a\", ssh = \"a\" },\n  { name = \"b\", ssh = \"b\",\n    shh = 1 },\n]\n", 4, "unknown_key", `unknown key "shh" in [[host]]`},
		{"a key of too many parts", "config.toml", "[ui]\n[" + strings.Repeat("a.", maxKeyParts) + "b]\n", 2, "too_large", "more than 64 parts"},
		{"a value nested too deep", "config.toml", "[ui]\nx = " + strings.Repeat("[", maxNesting+1) + strings.Repeat("]", maxNesting+1) + "\n", 2, "too_large", "more than 64 levels"},
		{"after a byte order mark", "config.toml", "\xef\xbb\xbf[ui]\nmotoin = \"off\"\n", 2, "unknown_key", `unknown key "motoin" in [ui]`},
		{"a key with \\e", "config.toml", "[ui]\n\"mo\\etoin\" = \"off\"\n", 2, "unknown_key", `unknown key "mo\x1btoin" in [ui]`},
		{"a key with \\u", "config.toml", "[ui]\n\"mo\\u0074ion\" = \"fast\"\n", 2, "bad_value", `"motion" in [ui] must be "auto"`},
		{"too large", "config.toml", "# " + strings.Repeat("x", client.MaxConfigSize), 0, "too_large", "larger than 1 MiB"},
		{"busy lease", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\nmachine.provider = \"e2b\"\nmachine.busy_lease = \"0s\"\nmachine.id = \"a\"\n", 5, "bad_value", "must be a time"},
		{"provider", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\nmachine = { provider = \"lim\", id = \"a\" }\n", 4, "bad_value", `must be "lima", "docker" or "e2b", not "lim"`},
		{"keymap name", "config.toml", "[keymap]\n\"sesion: delete\" = \"x\"\n", 2, "bad_value", `"sesion: delete" in [keymap] is not a launcher command`},
		{"keymap key", "config.toml", "[keymap]\n\"session: rename\" = [\"r\", \"opt+e\"]\n", 2, "bad_value", `"opt" is not a modifier`},
		{"keymap order", "config.toml", "[keymap]\n\"host: add\" = \"shift+ctrl+a\"\n", 2, "bad_value", `write "ctrl+shift+a"`},
		{"keymap type", "config.toml", "[keymap]\n\"host: add\" = 5\n", 2, "wrong_type", "must be a key, a list of keys"},
		{"keymap capital", "config.toml", "[keymap]\n\"host: add\" = \"A\"\n", 2, "bad_value", `reads "A" as "a"`},
		{"detach key", "config.toml", "[attach]\ndetach_key = \"ctr+x\"\n", 2, "bad_value", `unknown modifier "ctr"`},
		{"paste key", "config.toml", "attach.image_paste_key = \"ctr+v\"\n", 1, "bad_value", `unknown modifier "ctr"`},
		{"secret", "config.toml", "[secrets]\nTOKEN = \" \"\n", 2, "bad_value", `"TOKEN" in [secrets] has an empty command`},
		{"host name", "hosts.toml", "[[host]]\nname = \"my box\"\nssh = \"a\"\n", 2, "bad_host", `"my box" cannot name a host`},
		{"no host name", "hosts.toml", "[[host]]\nssh = \"a\"\n", 1, "bad_host", "a [[host]] has no name"},
		{"two hosts", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \"a\"\n\n[[host]]\nssh = \"b\"\nname = \"a\"\n", 7, "bad_host", `two hosts are named "a"`},
		{"empty ssh", "hosts.toml", "[[host]]\nname = \"a\"\nssh = \" \"\n", 3, "bad_host", `the host "a" has no ssh target`},
		{"no ssh", "hosts.toml", "[[host]]\nname = \"a\"\n", 1, "bad_host", `the host "a" has no ssh target`},
	} {
		t.Run(c.name, func(t *testing.T) {
			configHome(t)(c.file, c.body)
			r := checkConfig()
			if len(r.Findings) != 1 || r.OK {
				t.Fatalf("%q gives %+v", c.body, r.Findings)
			}
			f := r.Findings[0]
			if f.Line != c.line || f.Code != c.code || f.Severity != severityError || !strings.Contains(f.Message, c.has) || !strings.HasSuffix(f.Path, "/"+c.file) {
				t.Errorf("%q gives %+v, want line %d, %s, %q", c.body, f, c.line, c.code, c.has)
			}
		})
	}
}

// Each kind of host, as the client saves it, passes: a host of `host
// create` needs no ssh, because its provider reaches it.
func TestConfigCheckSavedHosts(t *testing.T) {
	configHome(t)
	hosts := []client.Host{
		{Name: "box", SSH: "me@box:2222", SSHConfig: "~/src/app/ssh_config", Labels: map[string]string{"provider": "gcp"}, Secrets: []string{"TOKEN"}},
		{Name: "gcp", SSH: "gcp-dev", Provider: &client.Provider{Script: "~/.config/providers/gcp.sh", WakeOnAttach: true}},
		// As internal/provider/lima, docker and e2b write them.
		{Name: "lima", SSH: "lima-" + brand.Name + "-lima", SSHConfig: "/home/me/.lima/x/ssh.config", Machine: &client.Machine{Provider: "lima", ID: brand.Name + "-lima", IdleStop: "30m"}},
		{Name: "docker", Machine: &client.Machine{Provider: "docker", ID: brand.Name + "-docker", User: "agent", Image: "debian:stable-slim"}},
		{Name: "e2b", Machine: &client.Machine{Provider: "e2b", ID: "i1x2y3", User: "user", Template: "agents", TokenCommand: "op read op://dev/e2b/key"}},
		{Name: "e2b-idle", Machine: &client.Machine{Provider: "e2b", ID: "i4x5y6", User: "user", Template: "agents", IdleStop: "never", BusyLease: "2h", Stdin: "stream"}},
	}
	if err := client.SaveHosts(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	if r := checkConfig(); len(r.Findings) != 0 {
		t.Errorf("hosts that the client saved give %+v", r.Findings)
	}
}

// A key matches its field as the TOML package matches it, in any case, so
// config check and the settings agree.
func TestConfigCheckCase(t *testing.T) {
	write := configHome(t)
	write("config.toml", "[UI]\nMotion = \"off\"\n[Attach]\nSCROLLBACK = \"clear\"\n")
	write("hosts.toml", "[[HOST]]\nNAME = \"a\"\nSsh = \"a\"\n")
	if r := checkConfig(); len(r.Findings) != 0 {
		t.Errorf("keys in another case give %+v", r.Findings)
	}
	if s := client.LoadSettings(); s.UI.Motion != "off" || s.Attach.Scrollback != "clear" {
		t.Errorf("the settings read %+v", s)
	}
	if h, err := client.LoadHosts(); err != nil || len(h) != 1 || h[0].Name != "a" || h[0].SSH != "a" {
		t.Errorf("the hosts read %+v, %v", h, err)
	}
}

// The check takes time in step with the size of a file, which has a
// limit: the two files of the review that took seconds take less than one.
func TestConfigCheckStaysQuick(t *testing.T) {
	write := configHome(t)
	var hosts strings.Builder
	for i := range 40000 {
		fmt.Fprintf(&hosts, "[[host]]\nname = \"h%d\"\nssh = \"h%d\"\nshh = 1\n", i, i)
	}
	write("hosts.toml", hosts.String())
	write("config.toml", "["+strings.Repeat("a.", 8000)+"b]\n")
	start := time.Now()
	r := checkConfig()
	if d := time.Since(start); d > quick {
		t.Errorf("config check took %v", d)
	}
	if len(r.Findings) != 2 || r.Findings[0].Code != "too_large" || r.Findings[1].Code != "too_large" {
		t.Errorf("findings %+v", r.Findings)
	}
}

// Files under the size limit that the second review built to take time or
// memory out of all proportion: they take less than a second, and memory
// in step with their size.
func TestConfigCheckStaysSmall(t *testing.T) {
	long := strings.Repeat("a", 500000)
	var keys strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&keys, "k%05d = 1\n", i)
	}
	for _, c := range []struct {
		name, file, body string
		codes            string
	}{
		// A long table name, then many keys: each key costs its own name.
		{"a long table", "config.toml", "[" + long + "]\n" + keys.String(), "too_large"},
		{"long keys of a free map", "config.toml", "[keymap]\n\"" + long + "\" = \"x\"\n[secrets]\n" + long[:400000] + " = \"\"\n", "bad_value bad_value"},
		// Arrays in arrays, as deep as the file allows: the scan stops at
		// maxNesting levels.
		{"deep arrays", "config.toml", "x = " + strings.Repeat("[", 450000) + strings.Repeat("]", 450000) + "\n", "too_large"},
		{"deep inline tables", "hosts.toml", "x = " + strings.Repeat("{a=", 200000) + "1" + strings.Repeat("}", 200000) + "\n", "too_large"},
	} {
		t.Run(c.name, func(t *testing.T) {
			configHome(t)(c.file, c.body)
			if len(c.body) > client.MaxConfigSize {
				t.Fatalf("the file has %d bytes, over the limit", len(c.body))
			}
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			start := time.Now()
			r := checkConfig()
			d := time.Since(start)
			runtime.ReadMemStats(&after)
			var codes []string
			for _, f := range r.Findings {
				codes = append(codes, f.Code)
			}
			if got := strings.Join(codes, " "); got != c.codes {
				t.Errorf("findings %s, want %s", got, c.codes)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; d > quick || alloc > 64<<20 {
				t.Errorf("config check took %v and %d MiB for a file of %d KiB", d, alloc>>20, len(c.body)>>10)
			}
		})
	}
}

// An unknown key and a launcher command get the nearest names.
func TestConfigCheckSuggests(t *testing.T) {
	configHome(t)("config.toml", "[attach]\ndetach_kye = \"ctrl+]\"\n[keymap]\n\"sesion: delete\" = \"x\"\n")
	r := checkConfig()
	if len(r.Findings) != 2 || strings.Join(r.Findings[0].Suggestions, ",") != "detach_key" || strings.Join(r.Findings[1].Suggestions, ",") != "session: delete" {
		t.Errorf("findings %+v", r.Findings)
	}
}

// An old name of a launcher command warns, and a warning alone exits with
// 0.
func TestConfigCheckOldName(t *testing.T) {
	oldKeymapNames["session: view"] = "session: rename"
	defer delete(oldKeymapNames, "session: view")
	configHome(t)("config.toml", "[keymap]\n\n\"session: view\" = \"v\"\n")
	out, err := run(t, "config", "check")
	if err != nil || !strings.Contains(out, "config.toml:3: warning: \"session: view\" is an old name. Use \"session: rename\".\n") ||
		!strings.Contains(out, "1 warning in 1 file.\n") {
		t.Errorf("config check = %v:\n%s", err, out)
	}
}

// A folder is refused as a config file on every supported platform.
func TestConfigCheckRefusesDirectory(t *testing.T) {
	configHome(t)
	if err := os.Mkdir(filepath.Join(client.ConfigDir(), "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "config", "check"); err == nil || !strings.Contains(out, "config.toml: cannot read it: it is not a regular file.\n") {
		t.Errorf("config check of a folder = %v:\n%s", err, out)
	}
}

// A file without read permission is a finding where Chmod can enforce it.
func TestConfigCheckUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows Chmod cannot revoke read permission")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	write := configHome(t)
	write("hosts.toml", "")
	if err := os.Chmod(filepath.Join(client.ConfigDir(), "hosts.toml"), 0); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "config", "check")
	if err == nil || !strings.Contains(out, "hosts.toml: cannot read it: permission denied.\n") {
		t.Errorf("config check = %v:\n%s", err, out)
	}
}

func TestGoldenConfigCheck(t *testing.T) {
	write := configHome(t)
	write("config.toml", `# Everything that config check finds, in one file.
[ui]
motoin = "on"

[attach]
detach_key = "ctr+x"
scrollback = "kept"

[sessions]
memory_max = "100M"

[agents]
inline = "yes"

[keymap]
"sesion: delete" = "x"
"session: rename" = ["r", "opt+e", 5]
"host: add" = "shift+ctrl+a"

[secrets]
EMPTY = ""
`)
	write("hosts.toml", `[[host]]
name = "box"
ssh = "box"

[[host]]
name = "box"
ssh = ""
sshh = "x"

[host.machine]
provider = "lim"
idle_stop = "5x"
stdin = "pipe"

[[host]]
name = "bad name"
`)
	var b bytes.Buffer
	checkConfig().write(&b)
	asJSON, err := versioned(checkConfig())
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{"config_check.golden": b.String(), "config_check_json.golden": string(asJSON)} {
		golden := filepath.Join("testdata", name)
		if *update {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("the report differs from %s (run go test ./internal/cli -run TestGoldenConfigCheck -update, and read the diff):\n%s", golden, got)
		}
	}
}

// --json prints the report as one object on stdout, as cli-spec 13.5
// gives it: "v", "ok", the files, the findings without a full stop, and
// "layers". The exit code is the same, and stderr stays empty.
func TestConfigCheckJSON(t *testing.T) {
	configHome(t)("config.toml", "[ui]\nmotoin = \"on\"\nmotion = \n")
	out, err := run(t, "config", "check", "--json")
	var r struct {
		V        int  `json:"v"`
		OK       bool `json:"ok"`
		Files    []checkedFile
		Findings []map[string]any
		Layers   []any `json:"layers"`
	}
	if jerr := json.Unmarshal([]byte(out), &r); jerr != nil {
		t.Fatalf("not JSON: %v\n%s", jerr, out)
	}
	if r.V != 1 || r.OK || len(r.Files) != 2 || r.Layers == nil || len(r.Findings) != 1 || !strings.Contains(out, `"layers": []`) {
		t.Errorf("the report is %s", out)
	}
	f := r.Findings[0]
	if f["code"] != "toml_syntax" || f["line"] != 3.0 || f["column"] == nil || strings.HasSuffix(f["message"].(string), ".") {
		t.Errorf("the finding is %v", f)
	}
	var b bytes.Buffer
	if code := finish(&b, err, nil, false, false, true); code != 1 || b.Len() != 0 {
		t.Errorf("config check --json exits with %d, and prints %q on stderr", code, b.String())
	}
	configHome(t)
	if out := ok(t, "config", "check", "--json"); !strings.Contains(out, `"ok": true`) {
		t.Errorf("no files give %s", out)
	}
}

// config check takes and refuses the same idle times as host idle: one
// rule, from C6's parseIdle.
func TestIdleTimesAgree(t *testing.T) {
	for _, v := range []string{"30m", " 2h ", "1m", "60s", "never", "NEVER", " Never ", "30s", "59s", "0", "-1m", "soon", "never ever", "1h30m", "1m0.5s", "59.9s"} {
		_, perr := parseIdle(v)
		cerr := client.CheckIdleTime(v)
		if (perr == nil) != (cerr == nil) {
			t.Errorf("%q: host idle gives %v, and the client's rule %v", v, perr, cerr)
		}
		configHome(t)("hosts.toml", fmt.Sprintf("[[host]]\nname = \"a\"\n[host.machine]\nprovider = \"lima\"\nidle_stop = %q\nid = \"a\"\n", v))
		if found := len(checkConfig().Findings) > 0; found != (perr != nil) {
			t.Errorf("%q: config check finds %v, and host idle gives %v", v, found, perr)
		}
	}
}

// A config.toml that does not decode gives one warning, and the defaults.
// config check and the shell's completion print none.
func TestBrokenSettingsWarn(t *testing.T) {
	configHome(t)("config.toml", "[ui]\nmotion = \"off\"\n[attach]\nscrollback = \n")
	if s := client.LoadSettings(); s.UI.Motion != "auto" || s.Attach.Scrollback != "keep" {
		t.Errorf("a broken config.toml gives %+v, want the defaults", s)
	}
	root := Root()
	complete := &cobra.Command{Use: cobra.ShellCompRequestCmd}
	root.AddCommand(complete)
	find := func(path ...string) *cobra.Command {
		c, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var b bytes.Buffer
	root.SetErr(&b)
	warnSettings(find("version"), nil)
	if want := "Warning: config.toml has an error on line 4, so its settings are not used.\nTry: " + brand.Name + " config check\n"; b.String() != want {
		t.Errorf("the warning is %q, want %q", b.String(), want)
	}
	b.Reset()
	if warnSettings(find("flip"), []string{"box/a"}); b.Len() == 0 {
		t.Error("flip to a session does not warn")
	}
	// The launcher shows the warning in a notice of its own.
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
	}{{find("config", "check"), nil}, {complete, nil}, {root, nil}, {find("flip"), nil}} {
		b.Reset()
		if warnSettings(c.cmd, c.args); b.Len() != 0 {
			t.Errorf("%s %q warns: %q", c.cmd.CommandPath(), c.args, b.String())
		}
	}
	// With --json, the warning is one JSON line.
	keymap := find("config", "keymap")
	if err := keymap.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	warnSettings(keymap, nil)
	want := `{"v":1,"warning":{"code":"config_ignored","message":"config.toml has an error on line 4, so its settings are not used","try":"` + brand.Name + ` config check"}}` + "\n"
	if b.String() != want {
		t.Errorf("the JSON warning is %q, want %q", b.String(), want)
	}
	configHome(t)("config.toml", "[ui]\nmotoin = \"off\"\n")
	b.Reset()
	if warnSettings(find("version"), nil); b.Len() != 0 {
		t.Errorf("an unknown key warns: %q", b.String())
	}
}

// A real command, run as the binary runs it, warns once on stderr of a
// broken config.toml. config check and the shell's completion do not.
func TestBrokenSettingsWarnThroughMain(t *testing.T) {
	configHome(t)("config.toml", "[ui]\nmotion = \n")
	old := os.Args
	defer func() { os.Args = old }()
	warning := "Warning: config.toml has an error on line 2, so its settings are not used.\nTry: " + brand.Name + " config check\n"
	for _, c := range []struct {
		args  []string
		warns int
	}{
		{[]string{"version"}, 1},
		{[]string{"config", "path"}, 1},
		{[]string{"config", "check"}, 0},
		{[]string{cobra.ShellCompRequestCmd, "con"}, 0},
	} {
		os.Args = append([]string{brand.Name}, c.args...)
		got, _ := captureStderr(t, Main)
		if n := strings.Count(got, warning); n != c.warns || strings.Count(got, "Warning:") != c.warns {
			t.Errorf("%q prints %d warnings, want %d:\n%s", c.args, n, c.warns, got)
		}
	}
}
