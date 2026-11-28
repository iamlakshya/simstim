package client

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestReadConfigFileBoundsAndNonRegular(t *testing.T) {
	dir := t.TempDir()

	// Missing file.
	data, exists, err := ReadConfigFile(filepath.Join(dir, "missing.toml"))
	if err != nil || exists || data != nil {
		t.Fatalf("missing file = (%v, %v, %v), want (nil, false, nil)", data, exists, err)
	}

	// Regular file at exact 1 MiB limit succeeds.
	exact := filepath.Join(dir, "exact.toml")
	buf := make([]byte, MaxConfigBytes)
	for i := range buf {
		buf[i] = '#'
	}
	if err := os.WriteFile(exact, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	got, exists, err := ReadConfigFile(exact)
	if err != nil || !exists || len(got) != MaxConfigBytes {
		t.Fatalf("exact 1 MiB = (len=%d, exists=%v, err=%v)", len(got), exists, err)
	}

	// File > 1 MiB fails with too_large.
	over := filepath.Join(dir, "over.toml")
	if err := os.WriteFile(over, append(buf, '#'), 0o600); err != nil {
		t.Fatal(err)
	}
	_, exists, err = ReadConfigFile(over)
	var re *ConfigReadError
	if !exists || !errors.As(err, &re) || re.Code != "too_large" {
		t.Fatalf("over 1 MiB = (exists=%v, err=%v), want too_large", exists, err)
	}

	// Directory is rejected as non-regular.
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	_, exists, err = ReadConfigFile(sub)
	if !exists || !errors.As(err, &re) || re.Code != "unreadable" || !re.NonRegular {
		t.Fatalf("directory = (exists=%v, err=%v), want unreadable non-regular", exists, err)
	}

	// FIFO on Unix must fail immediately without blocking.
	if runtime.GOOS != "windows" {
		fifo := filepath.Join(dir, "fifo.toml")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		_, exists, err = ReadConfigFile(fifo)
		if !exists || !errors.As(err, &re) || re.Code != "unreadable" || !re.NonRegular {
			t.Fatalf("fifo = (exists=%v, err=%v), want unreadable non-regular", exists, err)
		}
	}
}

func TestInspectTOMLStructuralBounds(t *testing.T) {
	// Key with 65 parts exceeds MaxConfigKeyParts (64).
	parts := make([]string, MaxConfigKeyParts+1)
	for i := range parts {
		parts[i] = "a"
	}
	deepKey := strings.Join(parts, ".") + " = 1\n"
	var s Settings
	_, _, err := DecodeConfigStrict("config.toml", []byte(deepKey), &s)
	var re *ConfigReadError
	if !errors.As(err, &re) || re.Code != "too_large" || !strings.Contains(re.Msg, "64 parts") {
		t.Fatalf("65-part key = %v, want too_large (64 parts)", err)
	}

	// Value nested 65 array levels exceeds MaxConfigValueDepth (64).
	deepArr := "x = " + strings.Repeat("[", MaxConfigValueDepth+1) + strings.Repeat("]", MaxConfigValueDepth+1) + "\n"
	_, _, err = DecodeConfigStrict("config.toml", []byte(deepArr), &s)
	if !errors.As(err, &re) || re.Code != "too_large" || !strings.Contains(re.Msg, "64 levels") {
		t.Fatalf("65-level array = %v, want too_large (64 levels)", err)
	}

	// Value nested > 64 mixed array/inline-table levels (33 pairs = 66 levels,
	// 34 key parts) exceeds MaxConfigValueDepth (64) without hitting key-parts first.
	deepTbl := "x = " + strings.Repeat("[{ a = ", 33) + "1" + strings.Repeat(" }]", 33) + "\n"
	_, _, err = DecodeConfigStrict("config.toml", []byte(deepTbl), &s)
	if !errors.As(err, &re) || re.Code != "too_large" || !strings.Contains(re.Msg, "64 levels") {
		t.Fatalf("66-level mixed array/inline-table = %v, want too_large (64 levels)", err)
	}

	// UTF-8 BOM is accepted and keeps line 1 positions intact.
	bomDoc := append([]byte{0xEF, 0xBB, 0xBF}, []byte("[ui]\nmotion = \"off\"\n")...)
	var s2 Settings
	strictErr, ins, err := DecodeConfigStrict("config.toml", bomDoc, &s2)
	if err != nil || strictErr != nil || s2.UI.Motion != "off" || ins.Pos("ui", "motion").Line != 2 {
		t.Fatalf("BOM decode = (strict=%v, err=%v, motion=%q, line=%d)", strictErr, err, s2.UI.Motion, ins.Pos("ui", "motion").Line)
	}
}

func TestCaseFoldingAliasesAndFreeMaps(t *testing.T) {
	// Case-folded keys in the same struct table trigger an alias finding on the
	// second key in sorted order ("Motion" < "motion", so "motion"'s line).
	doc := "[ui]\nMotion = \"on\"\nmotion = \"off\"\n"
	var s Settings
	_, ins, err := DecodeConfigStrict("config.toml", []byte(doc), &s)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if len(ins.Aliases) != 1 {
		t.Fatalf("aliases = %+v, want 1", ins.Aliases)
	}
	a := ins.Aliases[0]
	if a.Table != "ui" || a.Canonical != "motion" || a.First != "Motion" || a.Second != "motion" || a.Pos.Line != 3 {
		t.Fatalf("alias = %+v, want ui.motion Motion/motion at line 3", a)
	}

	// Free maps ([keymap], [secrets], [host.labels]) are NOT case-folded.
	freeDoc := "[secrets]\nAPI_KEY = \"echo a\"\napi_key = \"echo b\"\n"
	var s2 Settings
	_, ins2, err := DecodeConfigStrict("config.toml", []byte(freeDoc), &s2)
	if err != nil {
		t.Fatalf("unexpected decode error on free map: %v", err)
	}
	if len(ins2.Aliases) != 0 {
		t.Fatalf("free map should not fold aliases, got %+v", ins2.Aliases)
	}

	// Separate [[host]] entries using different cases for their own keys are
	// not an alias within one host, whereas one [[host]] using both is.
	twoHosts := "[[host]]\nname = \"a\"\nssh = \"a\"\n\n[[host]]\nName = \"b\"\nssh = \"b\"\n"
	var hf hostsFile
	_, insH, err := DecodeConfigStrict("hosts.toml", []byte(twoHosts), &hf)
	if err != nil || len(insH.Aliases) != 0 {
		t.Fatalf("separate hosts should not alias: err=%v aliases=%+v", err, insH.Aliases)
	}
}

func TestLoadSettingsCheckedAndLoadHostsContracts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	// Missing config.toml -> defaults, no warning.
	s, warn := LoadSettingsChecked()
	if warn != "" || s.UI.Motion != "auto" {
		t.Fatalf("missing config = (%+v, %q)", s, warn)
	}

	// Unknown key does NOT discard config.toml.
	if err := os.WriteFile(SettingsPath(), []byte("[ui]\nmotion = \"off\"\nmotoin = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn = LoadSettingsChecked()
	if warn != "" || s.UI.Motion != "off" {
		t.Fatalf("unknown key should keep settings: (%+v, %q)", s, warn)
	}

	// Syntax error discards config.toml and warns with line number.
	if err := os.WriteFile(SettingsPath(), []byte("[ui]\nmotion = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn = LoadSettingsChecked()
	if s.UI.Motion != "auto" || !strings.Contains(warn, "line 2") || !strings.Contains(warn, "not used") {
		t.Fatalf("syntax error = (%+v, %q)", s, warn)
	}

	// Case-folding alias discards config.toml and names both spellings.
	if err := os.WriteFile(SettingsPath(), []byte("[ui]\nmotion = \"off\"\nMotion = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn = LoadSettingsChecked()
	if s.UI.Motion != "auto" || !strings.Contains(warn, `"Motion"`) || !strings.Contains(warn, `"motion"`) {
		t.Fatalf("alias warning = (%+v, %q)", s, warn)
	}

	// doctor's LocalChecks reports unknown keys and points to config check.
	if err := os.WriteFile(SettingsPath(), []byte("[ui]\nmotoin = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := LocalChecks()
	var found Check
	for _, c := range checks {
		if c.Name == "config.toml" {
			found = c
		}
	}
	if found.OK || !strings.Contains(found.Detail, "ui.motoin") || found.Fix != brand.Name+" config check" {
		t.Fatalf("LocalChecks config.toml = %+v", found)
	}
}
