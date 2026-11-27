package supervisor

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestColdPackagePlanIsFixedAcrossPolicyEditsRetriesAndMembers(t *testing.T) {
	mode, age := "strict", "1d"
	policy := &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{PackageManagers: &mode}, Network: api.NetworkValues{MinReleaseAge: &age}}}
	root := &Supervisor{lifetime: t.Context(), m: &host.Manifest{Wall: &host.WallRecord{Level: "process"}, Policy: policy}}
	if err := root.prepareBootPackageSettings(t.Context()); err != nil {
		t.Fatal(err)
	}
	original := proto.ClonePackageManagersV1(root.bootPackageSettings)
	mode, age = "off", "7d"
	if err := root.prepareBootPackageSettings(t.Context()); err != nil || !reflect.DeepEqual(original, root.bootPackageSettings) {
		t.Fatal("a policy edit or retry replaced the cold-start plan", err)
	}
	child := &Supervisor{primary: root}
	var args proto.CtlStartArgs
	if err := child.bootPackageSettingsArgs(&args); err != nil || !reflect.DeepEqual(args.PackageManagers, original) {
		t.Fatal("a member did not receive the same installed intent", err)
	}
	args.PackageManagers.Mode = "off"
	if root.bootPackageSettings.Mode != "strict" {
		t.Fatal("a member argument borrowed the root's plan")
	}
	if view, err := root.packageSettingsView(); err != nil || view != nil {
		t.Fatal("a prospective plan was reported as installed", err)
	}
	root.bootPackageSettingsGuest = true // Isolates publication after the actual guest capability/ACK.
	if err := root.markPackageSettingsInstalled(); err != nil {
		t.Fatal(err)
	}
	root.rec.phase = host.LifeRunning
	view, err := child.packageSettingsView()
	if err != nil || !reflect.DeepEqual(view, original) || !slices.Contains(root.sessionFeatures(), proto.FeaturePackageManagersV1) {
		t.Fatal("the live installed view lost the original plan", err)
	}
	view.MinReleaseAge = "365d"
	if root.bootPackageSettings.MinReleaseAge != "1d" {
		t.Fatal("a public view mutated the installed plan")
	}
	root.rec.phase = host.LifeStopped
	if view, err := child.packageSettingsView(); err != nil || view != nil {
		t.Fatal("a stopped session reported a live installed plan", err)
	}
}

func TestBootPackagePlanUsesOnlyAbsentDefaultsAndCanonicalRefusals(t *testing.T) {
	stamp := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	plan, err := bootPackageSettingsPlan(&api.Policy{}, stamp)
	if err != nil || plan.Mode != "guarded" || plan.MinReleaseAge != "7d" || plan.StartedAt != "2026-11-15T12:00:00Z" {
		t.Fatal("known absent defaults did not use the canonical plan", err)
	}
	for _, test := range []struct{ mode, age, code string }{{"", "7d", "package_level_invalid"}, {"off", "90m", "release_age_invalid"}, {"guarded", "366d", "release_age_invalid"}} {
		var detail *proto.Error
		_, err := bootPackageSettingsPlan(&api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{PackageManagers: &test.mode}, Network: api.NetworkValues{MinReleaseAge: &test.age}}}, stamp)
		if !errors.As(err, &detail) || detail.Code != test.code {
			t.Fatal("the actual plan builder bypassed canonical validation", err)
		}
	}
	if _, err := bootPackageSettingsPlan(nil, stamp); err == nil {
		t.Fatal("an unknown original merged policy became default authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &Supervisor{m: &host.Manifest{Wall: &host.WallRecord{Level: "process"}, Policy: &api.Policy{}}}
	if err := s.prepareBootPackageSettings(ctx); !errors.Is(err, context.Canceled) || s.bootPackageSettingsPrepared {
		t.Fatal("cancelled preparation fixed a new package plan", err)
	}
	if err := s.bootPackageSettingsArgs(&proto.CtlStartArgs{}); err == nil {
		t.Fatal("an unprepared supported wall omitted package settings")
	}
}

func TestActualStateAndInputPlanningExcludeGeneratedPackagePathsEvenWhenOff(t *testing.T) {
	mode, caches := "off", false
	m := &host.Manifest{Files: &host.FilePlan{State: files.StatePlan{Files: []string{".bunfig.toml", ".agent/state"}}}, Policy: &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{PackageManagers: &mode, KeepCaches: &caches}}}}
	state, err := host.SandboxStatePlan(m)
	if err != nil || !slices.Equal(state.Files, []string{".agent/state"}) {
		t.Fatal("the real retention producer kept generated package configuration", err)
	}
	s := &Supervisor{lifetime: t.Context(), m: m}
	protected, err := s.protectedInputPaths()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := proto.PackageManagerPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range paths {
		if !slices.Contains(protected, host.GuestHome+"/"+name) {
			t.Fatal("the actual input planner omitted a generated package path", name)
		}
	}
	m.Files.State = files.StatePlan{Dirs: []string{".bunfig.toml"}}
	if _, err := host.SandboxStatePlan(m); err == nil {
		t.Fatal("a direct retained bind overlapped generated package state")
	}
}
