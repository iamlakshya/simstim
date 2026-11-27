package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTypedSecretDoctorRefusesUnusedInvalidMetadataBeforeCommandChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the owned executable fixture uses a POSIX shell")
	}
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	programs := t.TempDir()
	ssh := "#!/bin/sh\nprintf executed > " + ShellWord(marker) + "\n"
	if err := os.WriteFile(filepath.Join(programs, "ssh"), []byte(ssh), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", programs)
	text := "[secrets.WANTED]\nrun = 'printf wanted'\n[secrets.UNUSED]\nrun = 'printf unused'\nhosts = []\n"
	if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range LocalChecks() {
		if check.Name == "secrets" && !check.OK {
			found = true
		}
		if check.Name == "ssh" {
			t.Fatal("doctor started command checks before full metadata validation")
		}
	}
	if !found {
		t.Fatal("doctor lost the actual global settings refusal")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestTypedSecretDoctorUsesTheCanonicalTableRunWithoutFetchingValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the owned executable fixture uses a POSIX shell")
	}
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	programs := t.TempDir()
	if err := os.WriteFile(filepath.Join(programs, "ssh"), []byte("#!/bin/sh\nprintf fixture-version\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	reader := "#!/bin/sh\nprintf executed > " + ShellWord(marker) + "\n"
	if err := os.WriteFile(filepath.Join(programs, "fixture-reader"), []byte(reader), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", programs)
	text := "[secrets.KEY]\nrun = '  fixture-reader ignored-argument  '\non_hosts = []\n"
	if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range LocalChecks() {
		if check.Name == "secret KEY" {
			found = true
			if !check.OK || !strings.Contains(check.Detail, "fixture-reader") {
				t.Fatal("doctor ignored the canonical structured run command")
			}
		}
	}
	if !found {
		t.Fatal("doctor dropped a structured command because on_hosts was empty")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestTypedSecretFileCheckSharesBoundedReadAliasAndSemanticValidation(t *testing.T) {
	isolateTypedSecretSettings(t)
	for _, text := range []string{
		"[ui]\nmotion = 'off'\nMOTION = 'on'\n",
		"[secrets.KEY]\nrun = 'printf one'\nRun = 'printf two'\n",
		"[secrets.KEY]\nrun = 'printf key'\nhosts = ['example.com:80']\nheader = 'Authorization'\nformat = 'Bearer {value}'\n",
		strings.Repeat("#", MaxConfigSize+1),
	} {
		if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if check := fileCheck("config.toml", settingsPath(), &Settings{}); check.OK {
			t.Fatal("doctor bypassed the canonical reader, alias rule or complete secret normalisation")
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Remove(settingsPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(settingsPath()+".missing", settingsPath()); err != nil {
			t.Fatal(err)
		}
		if check := fileCheck("config.toml", settingsPath(), &Settings{}); check.OK {
			t.Fatal("doctor treated a broken config link as an absent valid file")
		}
	}

}
