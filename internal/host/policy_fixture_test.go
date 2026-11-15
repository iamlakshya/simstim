package host

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func policyError(t *testing.T, err error, code string) {
	t.Helper()
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code == "" || pe.Hint == "" || (code != "" && pe.Code != code) {
		t.Fatalf("error = %v, want %q with a hint", err, code)
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" && strings.Contains(pe.Message, state) {
		t.Fatalf("error exposes the state folder: %v", err)
	}
}

func savedPolicyManifest(t *testing.T) *Manifest {
	t.Helper()
	secretHome(t)
	zero := 0
	m := &Manifest{Schema: 1, ID: "abcd2345", Name: "box", CreatedAt: time.Now().UTC(),
		Agent:   AgentInfo{Kind: "custom", Argv: []string{"/bin/sh"}},
		Status:  Status{State: StateExited, ExitCode: &zero, Source: "process", Claimed: true},
		Guest:   &GuestClaims{ExitCode: &zero},
		Restart: Restart{Policy: "never"},
		Wall:    &WallRecord{Level: "none", Driver: "plain"}}
	m.Cwd = t.TempDir()
	m.Env = []string{"PATH=/usr/bin:/bin"}
	m.Layers = []api.Layer{{Kind: api.LayerSession, Preset: api.PresetLocked}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	return m
}
