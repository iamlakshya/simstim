package client

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type countedSettingsFile struct {
	*os.File
	closes *int
}

func (f *countedSettingsFile) Close() error { (*f.closes)++; return f.File.Close() }

func TestRetainedSettingsOperationUsesOneRealDecodedSource(t *testing.T) {
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	body := "[secrets]\nTOKEN=" + strconv.Quote(secretSettingsMarkerCommand(marker)) + "\n"
	if err := os.WriteFile(settingsPath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	opens, closes := 0, 0
	old := openConfig
	openConfig = func(path string) (configFile, error) {
		opens++
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return &countedSettingsFile{File: f, closes: &closes}, nil
	}
	defer func() { openConfig = old }()
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	settings, err := operation.Settings(t.Context())
	if err != nil || settings.Secrets["TOKEN"].Run != secretSettingsMarkerCommand(marker) {
		t.Fatal("operation did not carry actual decoded settings", err)
	}
	snapshot, err := operation.Snapshot(t.Context())
	if err != nil || snapshot == nil || snapshot.file == nil {
		t.Fatal("operation lost original held descriptor", err)
	}
	source, err := newOrdinaryLocalGitMetadataSource(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckKeys(t.Context(), []string{"GIT_CONFIG_COUNT"}); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := operation.Revalidate(t.Context()); err != nil {
		t.Fatal("borrower closed original operation", err)
	}
	if err := operation.Close(); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || closes != 1 {
		t.Fatal("metadata opened/parsed a second source or lost close ownership", opens, closes)
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestOrdinaryMetadataProducerPreservesWholeConfigFailureAndFDMutation(t *testing.T) {
	isolateTypedSecretSettings(t)
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nTOKEN='unused'\n[secrets.UNUSED]\nrun=''\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if source, err := OpenLocalGitMetadataSource(t.Context()); err == nil || source != nil {
		t.Fatal("ordinary metadata invented names from invalid unused settings")
	}
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nTOKEN='unused'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := OpenLocalGitMetadataSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var user *UserError
	if err := source.CheckKeys(t.Context(), []string{"TOKEN"}); !errors.As(err, &user) || user.Code != "git_environment_secret" {
		t.Fatal("real configured name was not refused", err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nOTHER='changed-longer'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.Revalidate(t.Context()); err == nil {
		t.Fatal("metadata retained authority after original FD content changed")
	}
}

func TestOrdinaryMetadataNamesUseExactPOSIXAndASCIIWindowsComparison(t *testing.T) {
	for _, test := range []struct {
		a, b          string
		windows, want bool
	}{
		{"SystemRoot", "SYSTEMROOT", true, true}, {"SystemRoot", "SYSTEMROOT", false, false},
		{"SYSTEMROOT", "SYSTEMROOT", false, true}, {"ſYSTEMROOT", "SYSTEMROOT", true, false},
	} {
		if got := metadataNameEqual(test.a, test.b, test.windows); got != test.want {
			t.Fatal("native metadata name comparison changed", test)
		}
	}
}

func TestRetainedLoaderKeepsC9AliasesMapsAndKnownAbsence(t *testing.T) {
	isolateTypedSecretSettings(t)
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings, err := operation.Settings(t.Context()); err != nil || settings.loadErr != nil || settings.UI.Motion != "auto" {
		t.Fatal("genuine absence was not a checked default source", err)
	}
	if err := operation.Close(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"secrets=1\n", "[ui]\nMotion='auto'\nmotion='off'\n"} {
		if err := os.WriteFile(settingsPath(), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if operation, err := openSettingsOperation(t.Context()); err == nil || operation != nil {
			t.Fatal("unusable config became a successful retained source")
		}
		settings, warning := ReadSettings()
		if warning == "" || settings.UI.Motion != "auto" || settings.loadErr == nil {
			t.Fatal("ordinary C9 warning/default path lost its security error")
		}
		if names, err := ConfiguredSecretNames(settings); err == nil || names != nil {
			t.Fatal("failed read became successful empty names")
		}
		if strings.Contains(warning, "command-marker") {
			t.Fatal("warning exposed a configured command")
		}
	}
}
