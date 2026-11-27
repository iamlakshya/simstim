package host

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestInstalledPackageSettingsViewUsesOnlyCompleteStartPlan(t *testing.T) {
	original := &proto.PackageManagersV1{Version: 1, Mode: "guarded", MinReleaseAge: "7d", StartedAt: "2026-11-15T12:00:00Z"}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePackageSettingsView(raw)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("view = %#v, %v", got, err)
	}
	for _, invalid := range []string{
		`{}`, `{"v":1,"mode":"guarded","min_release_age":"7d","started_at":"2026-11-15T12:00:00Z","path":"/foreign"}`,
		`{"v":1,"v":1,"mode":"guarded","min_release_age":"7d","started_at":"2026-11-15T12:00:00Z"}`,
		`{"v":1,"mode":"guarded","min_release_age":"7d","started_at":"2026-11-15T12:00:00+00:00"}`,
		`{"V":1,"mode":"guarded","min_release_age":"7d","started_at":"2026-11-15T12:00:00Z"}`,
	} {
		if plan, err := decodePackageSettingsView([]byte(invalid)); err == nil || plan != nil {
			t.Fatalf("accepted %s: %#v, %v", invalid, plan, err)
		}
	}
	if plan, err := decodePackageSettingsView([]byte("null")); plan != nil || err != nil {
		t.Fatalf("unknown view = %#v, %v", plan, err)
	}
}

func TestEndedOrUnwalledPackageViewDoesNotReuseSavedPlan(t *testing.T) {
	for _, row := range []SessionInfo{{Alive: false, Wall: &api.WallInfo{}}, {Alive: true}} {
		row.PackageManagers = &proto.PackageManagersV1{Version: 1, Mode: "strict", MinReleaseAge: "1d", StartedAt: "2026-11-15T12:00:00Z"}
		livePackageSettings(&row)
		if row.PackageManagers != nil {
			t.Fatal("a non-live wall reused a previous start plan")
		}
	}
}
