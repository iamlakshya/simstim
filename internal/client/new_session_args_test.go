package client

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestNewSessionArgsChecksTerminalNamespaceBeforeMetadataLookup(t *testing.T) {
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-run")
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nTERM="+strconv.Quote(secretSettingsMarkerCommand(marker))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "configured-name-must-not-leave-through-terminal-metadata")
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	args, err := PrepareNewSessionArgs(ctx, NewArgs{}, 80, 24)
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Code != "git_environment_secret" || args.Term != "" {
		t.Fatal("configured terminal namespace reached its actual metadata lookup", err)
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestNewSessionArgsKeepsOriginalSettingsThroughDefaults(t *testing.T) {
	isolateTypedSecretSettings(t)
	if err := os.WriteFile(settingsPath(), []byte("[agents]\ninline=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	a, err := PrepareNewSessionArgs(ctx, NewArgs{}, 80, 24)
	if err != nil || !a.Inline || r.preparationSettings == nil {
		t.Fatal("same operation did not supply its real default", err)
	}
	original := r.preparationSettings
	if err := os.WriteFile(settingsPath(), []byte("[agents]\ninline=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNewSessionArgs(ctx, NewArgs{}, 80, 24); err == nil || r.preparationSettings != original {
		t.Fatal("changed settings renewed the original default-source operation", err)
	}
}
