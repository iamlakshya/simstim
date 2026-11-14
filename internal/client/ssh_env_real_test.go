package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRealSSHEnvironmentClearingIncludesTheSystemConfig(t *testing.T) {
	sshEnvDirs(t)
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), "Host *\n    SendEnv PRIVATE_TOKEN\n")
	sys := filepath.Join(t.TempDir(), "ssh_config")
	writeFile(t, sys, "Host *\n    SendEnv SYSTEM_TOKEN\n")
	oldSystem := systemConfig
	systemConfig = func() string { return sys }
	t.Cleanup(func() { systemConfig = oldSystem })
	h := Host{Name: "box", SSH: "box.invalid"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []Tier{TierOwn, TierShared} {
		if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
			h.Name: {Name: h.Name, Tier: tier},
		}}); err != nil {
			t.Fatal(err)
		}
		cfg, err := writeWrapperContext(t.Context(), h)
		if err != nil {
			t.Fatal(err)
		}
		vals := sshValues(t, []string{"-F", cfg})
		patterns := strings.Fields(strings.Join(vals["sendenv"], " "))
		if tier == TierOwn {
			if !slices.Contains(patterns, "PRIVATE_TOKEN") || !slices.Contains(patterns, "SYSTEM_TOKEN") {
				t.Fatalf("an own host lost its configured environment: %q", patterns)
			}
		} else if !slices.Equal(patterns, []string{"LANG", "LC_*"}) {
			t.Fatalf("the restricted host retained a user or system SendEnv: %q", patterns)
		}
	}
	cfg, err := DefaultSSHConfigContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vals := sshValues(t, []string{"-F", cfg})
	if patterns := strings.Fields(strings.Join(vals["sendenv"], " ")); !slices.Equal(patterns, []string{"LANG", "LC_*"}) {
		t.Fatalf("an unsaved target retained a user or system SendEnv: %q", patterns)
	}
}

func TestRealSSHEnvironmentRefusesSetEnvBeforeAnyConnection(t *testing.T) {
	sshEnvDirs(t)
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), "Host *\n    SetEnv PRIVATE_TOKEN=example-value\n")
	h := Host{Name: "box", SSH: "box.invalid"}
	cfg, err := writeWrapperContext(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	err = CheckSSHEnvironment(t.Context(), h.SSH, []string{"-F", cfg})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "ssh_setenv" || ue.Kind != KindRefused {
		t.Fatalf("effective SetEnv was not refused: %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE_TOKEN") || strings.Contains(err.Error(), "example-value") {
		t.Fatalf("the refusal exposed an environment value: %v", err)
	}
	if _, err := os.Stat(tiersPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking an unsaved target started migration: %v", err)
	}
}
