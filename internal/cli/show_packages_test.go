package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

func installedPackages() *proto.PackageManagersV1 {
	return &proto.PackageManagersV1{Version: 1, Mode: "guarded", MinReleaseAge: "7d", StartedAt: "2026-11-10T10:40:00Z"}
}

func TestShowPackagesKeepsInstalledSettingsDistinctFromPolicy(t *testing.T) {
	for _, test := range []struct {
		name, mode, age, want string
	}{
		{"guarded", "guarded", "7d", "guarded, 7 days (from current start)"},
		{"strict", "strict", "1d", "strict, 1 day (from current start)"},
		{"off", "off", "365d", "off, 365 days (from current start)"},
		{"zero", "guarded", "0d", "guarded, no minimum age (from current start)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := installedPackages()
			plan.Mode, plan.MinReleaseAge = test.mode, test.age
			row := client.Session{Alive: true, PackageManagers: plan}
			if got := showPackages(row); got != test.want {
				t.Fatalf("installed settings = %q, want %q", got, test.want)
			}
			if plan.Mode != test.mode || plan.MinReleaseAge != test.age {
				t.Fatal("show changed the installed start plan")
			}
		})
	}
}

func TestShowPackagesDoesNotGuessMissingOrInvalidSettings(t *testing.T) {
	if got := showPackages(client.Session{Alive: true}); got != "" {
		t.Fatalf("legacy session gained package settings: %q", got)
	}
	if got := showPackages(client.Session{Alive: true, Wall: &api.WallInfo{Level: "none"}}); got != "" {
		t.Fatalf("unwalled session gained package settings: %q", got)
	}
	for _, test := range []struct {
		name string
		edit func(*client.Session)
	}{
		{"missing", func(s *client.Session) { s.PackageManagers = nil }},
		{"stopped", func(s *client.Session) { s.Alive = false }},
		{"version", func(s *client.Session) { s.PackageManagers.Version = 2 }},
		{"mode", func(s *client.Session) { s.PackageManagers.Mode = "guarded\x1bc" }},
		{"age", func(s *client.Session) { s.PackageManagers.MinReleaseAge = "90m" }},
		{"time", func(s *client.Session) { s.PackageManagers.StartedAt = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := client.Session{Alive: true, Wall: &api.WallInfo{Level: "process"}, PackageManagers: installedPackages()}
			test.edit(&row)
			got := describeSession("box", row)
			if !strings.Contains(got, "Packages        unknown (no live start settings)") || strings.Contains(got, "7 days") || strings.ContainsRune(got, '\x1b') {
				t.Fatalf("missing/invalid plan became installed defaults: %q", got)
			}
		})
	}
}

func TestShowReadsPackageSettingsFromItsExistingSessionSnapshot(t *testing.T) {
	row := client.Session{ID: "pack2345", Name: "app", Alive: true, State: api.StateWorking, Wall: &api.WallInfo{Level: "process"}, PackageManagers: installedPackages()}
	_, transport := fakeBox(t, []client.Session{row})
	// A newer policy governs registry requests at once. It cannot replace
	// the package settings installed at this start, and show needs no requery.
	transport.Answers = map[string]any{"policy.ls": api.Policy{LayerValues: api.LayerValues{Network: api.NetworkValues{MinReleaseAge: new("1d")}}}}
	out := ok(t, "show", "box/pack2345")
	if !strings.Contains(out, "Packages        guarded, 7 days (from current start)") || strings.Contains(out, "1 day") {
		t.Fatalf("show substituted current policy for installed settings: %q", out)
	}
	for _, op := range transport.Ops() {
		if op != "ls" {
			t.Fatalf("show added a package policy query: %q", transport.Ops())
		}
	}
	var got struct {
		Packages *proto.PackageManagersV1 `json:"package_managers"`
	}
	if err := json.Unmarshal([]byte(okJSON(t, "show", "box/pack2345", "--json")), &got); err != nil || got.Packages == nil || *got.Packages != *row.PackageManagers {
		t.Fatalf("JSON changed the existing installed snapshot: %+v, %v", got, err)
	}
}
