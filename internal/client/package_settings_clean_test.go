package client

import (
	"github.com/iamlakshya/simstim/internal/proto"
	"testing"
)

func TestCleanSessionOwnsValidatedInstalledPackageSettings(t *testing.T) {
	plan := &proto.PackageManagersV1{Version: 1, Mode: "guarded", MinReleaseAge: "7d", StartedAt: "2026-11-15T12:00:00Z"}
	original := Session{ID: "s1", PackageManagers: plan}
	got := CleanSession(original)
	if got.PackageManagers == nil || *got.PackageManagers != *plan || got.PackageManagers == plan {
		t.Fatal("clean row lost or borrowed the installed plan")
	}
	got.PackageManagers.Mode = "off"
	if plan.Mode != "guarded" {
		t.Fatal("draw copy changed original installed plan")
	}
	for _, mode := range []string{"guarded\x1b[2J", "balanced", ""} {
		hostile := *plan
		hostile.Mode = mode
		input := Session{ID: "s1", PackageManagers: &hostile}
		clean := CleanSession(input)
		if clean.PackageManagers != nil || clean.ID != "s1" || input.PackageManagers.Mode != mode {
			t.Fatal("invalid plan was drawn or request row changed")
		}
	}
}
