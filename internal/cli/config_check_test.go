package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func runConfigCheckMain(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"client"}, args...)
	stdout, stderr = captureOutput(t, func() { code = Main() })
	return stdout, stderr, code
}

func writeConfigFiles(t *testing.T, configBody, hostsBody string) {
	t.Helper()
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if configBody != "" {
		if err := os.WriteFile(client.SettingsPath(), []byte(configBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if hostsBody != "" {
		if err := os.WriteFile(client.HostsPath(), []byte(hostsBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigCheckMissingAndClean(t *testing.T) {
	hosttest.New(t)

	// Both files missing: exit 0, prints "none yet".
	out, errOut, code := runConfigCheckMain(t, "config", "check")
	if code != 0 || errOut != "" || strings.Count(out, "none yet") != 2 {
		t.Fatalf("missing files: code=%d stdout=%q stderr=%q", code, out, errOut)
	}

	// Valid config.toml and hosts.toml (including a machine host without ssh).
	writeConfigFiles(t,
		"[ui]\nmotion = \"off\"\n\n[attach]\ndetach_key = \"ctrl+\\\\\"\nimage_paste_key = \"ctrl+v\"\nscrollback = \"clear\"\n\n[agents]\ninline = true\n\n[sessions]\nmemory_max = \"4G\"\n\n[keymap]\n\"session: delete\" = [\"d\", \"backspace\"]\n\"view: search\" = false\n\n[secrets]\nAPI_KEY = \"echo ok\"\n",
		"[[host]]\nname = \"box\"\nssh = \"dev@box\"\n\n[[host]]\nname = \"dk\"\n[host.machine]\nprovider = \"docker\"\nid = \"dk-1\"\nidle_stop = \"30m\"\nbusy_lease = \"1h\"\nstdin = \"send\"\n",
	)
	out, errOut, code = runConfigCheckMain(t, "config", "check")
	if code != 0 || errOut != "" || strings.Count(out, "no problems") != 2 {
		t.Fatalf("clean files: code=%d stdout=%q stderr=%q", code, out, errOut)
	}

	// --json on clean files passes checkJSON and reports ok=true.
	jout, jerr, jcode := runConfigCheckMain(t, "config", "check", "--json")
	if jcode != 0 || jerr != "" {
		t.Fatalf("clean json: code=%d stdout=%q stderr=%q", jcode, jout, jerr)
	}
	checkJSON(t, "config check --json", jout, false)
	var rep struct {
		V        int             `json:"v"`
		OK       bool            `json:"ok"`
		Files    []checkedFile   `json:"files"`
		Findings []configFinding `json:"findings"`
		Layers   []string        `json:"layers"`
	}
	if err := json.Unmarshal([]byte(jout), &rep); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, jout)
	}
	if rep.V != 1 || !rep.OK || len(rep.Files) != 2 || len(rep.Findings) != 0 {
		t.Fatalf("unexpected clean json report: %+v", rep)
	}
}

func TestConfigCheckAllRulesAndLines(t *testing.T) {
	t.Run("unknown key with line column suggestion and Try hint", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t, "\n\n[ui]\nmotoin = \"off\"\n", "")
		out, errOut, code := runConfigCheckMain(t, "config", "check")
		if code != 1 {
			t.Fatalf("code=%d, want 1\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		if !strings.Contains(out, `config.toml:4:1: unknown key "motoin" in [ui]. Did you mean "motion"?`) {
			t.Errorf("unexpected stdout:\n%s", out)
		}
		if !strings.Contains(out, "1 error in 2 files.") {
			t.Errorf("missing summary:\n%s", out)
		}
		if !strings.Contains(errOut, "Try:") || !strings.Contains(errOut, brand.Name+" config check") {
			t.Errorf("missing Try: hint on stderr:\n%s", errOut)
		}

		// JSON output has exact line, column, code, message, and suggestions.
		jout, jerr, jcode := runConfigCheckMain(t, "config", "check", "--json")
		if jcode != 1 || jerr != "" {
			t.Fatalf("json code=%d stderr=%q", jcode, jerr)
		}
		checkJSON(t, "config check --json (error)", jout, false)
		var rep configReport
		if err := json.Unmarshal([]byte(jout), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.OK || len(rep.Findings) != 1 {
			t.Fatalf("findings = %+v", rep.Findings)
		}
		f := rep.Findings[0]
		if f.Line != 4 || f.Column != 1 || f.Code != "unknown_key" || f.Severity != "error" ||
			f.Message != `unknown key "motoin" in [ui]` || len(f.Suggestions) != 1 || f.Suggestions[0] != "motion" {
			t.Fatalf("unexpected finding: %+v", f)
		}
	})

	t.Run("syntax error", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t, "", "[[host]]\nssh = \n")
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)
		if len(rep.Findings) != 1 || rep.Findings[0].Code != "toml_syntax" || rep.Findings[0].Line != 2 || rep.Findings[0].Column != 7 {
			t.Fatalf("syntax finding = %+v", rep.Findings)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t, "[agents]\ninline = \"yes\"\n", "")
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)
		if len(rep.Findings) != 1 || rep.Findings[0].Code != "wrong_type" || rep.Findings[0].Line != 2 {
			t.Fatalf("wrong_type finding = %+v", rep.Findings)
		}
	})

	t.Run("case-folding alias", func(t *testing.T) {
		hosttest.New(t)
		writeConfigFiles(t, "[ui]\nMotion = \"on\"\nmotion = \"off\"\n", "")
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)
		if len(rep.Findings) != 1 || rep.Findings[0].Code != "bad_value" || rep.Findings[0].Line != 3 ||
			!strings.Contains(rep.Findings[0].Message, `"Motion"`) || !strings.Contains(rep.Findings[0].Message, `"motion"`) {
			t.Fatalf("alias finding = %+v", rep.Findings)
		}
	})

	t.Run("settings value checks", func(t *testing.T) {
		hosttest.New(t)
		cfg := strings.Join([]string{
			"[ui]",
			`motion = "fast"`,
			"[attach]",
			`scrollback = "drop"`,
			`detach_key = "ctrl+nothing"`,
			`image_paste_key = "badkey"`,
			"[sessions]",
			`memory_max = "lots"`,
			"[keymap]",
			`"session: delet" = "d"`,
			`"session: delete" = ["ctrl+nothing", "alt+ctrl+e", "shift+a"]`,
			`"view: search" = 42`,
			"[secrets]",
			`EMPTY_SEC = "   "`,
			`"1BAD" = "echo x"`,
			"",
		}, "\n")
		writeConfigFiles(t, cfg, "")
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		checkJSON(t, "settings value checks", jout, false)
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)

		wantSub := []struct {
			line int
			code string
			sub  string
		}{
			{2, "bad_value", `ui.motion`},
			{4, "bad_value", `attach.scrollback`},
			{5, "bad_value", `attach.detach_key`},
			{6, "bad_value", `attach.image_paste_key`},
			{8, "bad_value", `sessions.memory_max`},
			{10, "bad_value", `unknown launcher command "session: delet"`},
			{11, "bad_value", `ctrl+nothing`},
			{11, "bad_value", `alt+ctrl+e`},
			{11, "bad_value", `shift+a`},
			{12, "wrong_type", `view: search`},
			{14, "bad_value", `empty command`},
			{15, "bad_value", `"1BAD"`},
		}
		for _, w := range wantSub {
			found := false
			for _, f := range rep.Findings {
				if f.Line == w.line && f.Code == w.code && strings.Contains(f.Message, w.sub) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("missing finding line=%d code=%s sub=%q in %+v", w.line, w.code, w.sub, rep.Findings)
			}
		}
	})

	t.Run("hosts value checks", func(t *testing.T) {
		hosttest.New(t)
		hosts := strings.Join([]string{
			"[[host]]",
			`name = "bad name"`,
			`ssh = "a"`,
			"",
			"[[host]]",
			`name = "dup"`,
			`ssh = "b"`,
			"",
			"[[host]]",
			`name = "dup"`,
			"",
			"[[host]]",
			`name = "vm1"`,
			"[host.machine]",
			`idle_stop = "10s"`,
			`busy_lease = "nope"`,
			`stdin = "fast"`,
			"[host.provider]",
			`script = "  "`,
			"",
		}, "\n")
		writeConfigFiles(t, "", hosts)
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		checkJSON(t, "hosts value checks", jout, false)
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)

		wantSub := []struct {
			line int
			code string
			sub  string
		}{
			{2, "bad_host", `"bad name"`},
			{10, "bad_host", `duplicate host name "dup"`},
			{9, "bad_host", `needs ssh`},
			{14, "bad_value", `without a provider`},
			{14, "bad_value", `without an id`},
			{15, "bad_value", `idle_stop`},
			{16, "bad_value", `busy_lease`},
			{17, "bad_value", `stdin`},
			{19, "bad_value", `without a script`},
		}
		for _, w := range wantSub {
			found := false
			for _, f := range rep.Findings {
				if f.Line == w.line && f.Code == w.code && strings.Contains(f.Message, w.sub) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("missing finding line=%d code=%s sub=%q in %+v", w.line, w.code, w.sub, rep.Findings)
			}
		}
	})

	t.Run("unreadable FIFO and oversized file", func(t *testing.T) {
		hosttest.New(t)
		if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			if err := syscall.Mkfifo(client.SettingsPath(), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		big := make([]byte, client.MaxConfigBytes+8)
		for i := range big {
			big[i] = '#'
		}
		if err := os.WriteFile(client.HostsPath(), big, 0o600); err != nil {
			t.Fatal(err)
		}
		jout, _, code := runConfigCheckMain(t, "config", "check", "--json")
		if code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		var rep configReport
		_ = json.Unmarshal([]byte(jout), &rep)
		codes := map[string]bool{}
		for _, f := range rep.Findings {
			codes[f.Code] = true
		}
		if !codes["too_large"] || (runtime.GOOS != "windows" && !codes["unreadable"]) {
			t.Fatalf("expected unreadable and too_large, got %+v", rep.Findings)
		}
	})

	t.Run("warning alone exits 0", func(t *testing.T) {
		hosttest.New(t)
		old := append([]oldForm(nil), oldForms...)
		t.Cleanup(func() { oldForms = old })
		oldForms = append(oldForms, oldForm{
			Old: "session: preview", New: "session: peek", Kind: "keymap name", Cmd: "config keymap", Version: "0.2.0", Code: "old_name",
		})
		writeConfigFiles(t, "[keymap]\n\"session: preview\" = \"space\"\n", "")
		out, errOut, code := runConfigCheckMain(t, "config", "check")
		if code != 0 || errOut != "" || !strings.Contains(out, `warning: "session: preview" is an old name; use "session: peek"`) {
			t.Fatalf("warning only: code=%d stdout=%q stderr=%q", code, out, errOut)
		}
	})

	t.Run("usage error exits 2", func(t *testing.T) {
		hosttest.New(t)
		_, errOut, code := runConfigCheckMain(t, "config", "check", "extra")
		if code != 2 || !strings.Contains(errOut, "Error:") {
			t.Fatalf("extra arg: code=%d stderr=%q", code, errOut)
		}
	})
}

func TestBrokenConfigWarnsOnceBeforeOtherCommands(t *testing.T) {
	hosttest.New(t)
	writeConfigFiles(t, "[ui]\nmotion = \n", "")

	// A normal command prints one warning on stderr and succeeds with defaults.
	out, errOut, code := runConfigCheckMain(t, "version")
	if code != 0 || !strings.Contains(out, brand.Name) {
		t.Fatalf("version: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "config.toml has an error on line 2, so its settings are not used") ||
		!strings.Contains(errOut, brand.Name+" config check") {
		t.Fatalf("expected broken config warning on stderr, got %q", errOut)
	}

	// With --json, the warning on stderr is one JSON line.
	jout, jerr, jcode := runConfigCheckMain(t, "version", "--json")
	if jcode != 0 || !json.Valid([]byte(jout)) {
		t.Fatalf("version --json: code=%d stdout=%q stderr=%q", jcode, jout, jerr)
	}
	if !json.Valid([]byte(strings.TrimSpace(jerr))) || !strings.Contains(jerr, `"code":"bad_config"`) {
		t.Fatalf("expected JSON warning line on stderr, got %q", jerr)
	}
}

func TestGoldenConfigCheck(t *testing.T) {
	hosttest.New(t)
	// Point XDG_CONFIG_HOME inside $HOME so AbbrevPath uses ~/.config/...
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	writeConfigFiles(t, "[ui]\nmotoin = \"off\"\n", "[[host]]\nname = \"box\"\nssh = \"dev@box\"\n")

	out, errOut, code := runConfigCheckMain(t, "config", "check")
	if code != 1 {
		t.Fatalf("text code=%d", code)
	}
	jout, jerr, jcode := runConfigCheckMain(t, "config", "check", "--json")
	if jcode != 1 || jerr != "" {
		t.Fatalf("json code=%d stderr=%q", jcode, jerr)
	}
	checkJSON(t, "golden config check --json", jout, false)

	got := "== text stdout\n" + out + "== text stderr\n" + errOut + "== json stdout\n" + jout
	got = strings.ReplaceAll(got, brand.Name, "<name>")
	golden := filepath.Join("testdata", "config_check.golden")
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
		t.Errorf("config check output differs from %s:\n%s", golden, got)
	}
}

func TestConfigCheckPackageManagerSettings(t *testing.T) {
	hosttest.New(t)
	writeConfigFiles(t, "[sandbox]\npackage_managers = \"on\"\n\n[network]\nmin_release_age = \"90m\"\n", "")

	out, _, code := runConfigCheckMain(t, "config", "check", "--json")
	if code != 1 {
		t.Fatalf("code = %d, want 1\n%s", code, out)
	}
	checkJSON(t, "config check --json package errors", out, false)
	var rep configReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out)
	}
	if rep.OK {
		t.Fatalf("expected OK=false: %+v", rep)
	}
	var sawLevel, sawAge bool
	for _, f := range rep.Findings {
		if f.Code == "package_level_invalid" && f.Line == 2 {
			sawLevel = true
		}
		if f.Code == "release_age_invalid" && f.Line == 5 {
			sawAge = true
		}
	}
	if !sawLevel || !sawAge {
		t.Fatalf("findings = %+v, want package_level_invalid (line 2) and release_age_invalid (line 5)", rep.Findings)
	}

	writeConfigFiles(t, "[sandbox]\npackage_managers = \"strict\"\npull_max = \"1G\"\n\n[sandbox.env]\nGOPROXY = \"https://corp.example/go\"\n\n[network]\nmin_release_age = \"7d\"\nmodel_budget = 2000000\nupload_budget = \"16M\"\n", "")
	out, _, code = runConfigCheckMain(t, "config", "check", "--json")
	if code != 0 {
		t.Fatalf("valid package/budget config failed: code=%d\n%s", code, out)
	}

	writeConfigFiles(t, "[sandbox]\npull_max = \"nope\"\n\n[network]\nmodel_budget = -50\nupload_budget = \"nope\"\n", "")
	out, _, code = runConfigCheckMain(t, "config", "check", "--json")
	if code != 1 {
		t.Fatalf("invalid budget config code = %d, want 1\n%s", code, out)
	}
	var budgetRep configReport
	if err := json.Unmarshal([]byte(out), &budgetRep); err != nil {
		t.Fatalf("decode budget report: %v\n%s", err, out)
	}
	var sawModel, sawUpload, sawPullMax bool
	for _, f := range budgetRep.Findings {
		if f.Code == "pull_max_invalid" && f.Line == 2 {
			sawPullMax = true
		}
		if f.Code == "model_budget_invalid" && f.Line == 5 {
			sawModel = true
		}
		if f.Code == "upload_budget_invalid" && f.Line == 6 {
			sawUpload = true
		}
	}
	if !sawPullMax || !sawModel || !sawUpload {
		t.Fatalf("budget findings = %+v, want pull_max_invalid (line 2), model_budget_invalid (line 5) and upload_budget_invalid (line 6)", budgetRep.Findings)
	}
}
