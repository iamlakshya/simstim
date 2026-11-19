package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRequiredPackagePlanRefusesMissingDataBeforeInfrastructureWrites(t *testing.T) {
	root := t.TempDir()
	g := &Init{root: root, packageSettingsRequired: true}
	if err := g.prepareSession(&proto.CtlStartArgs{}); err == nil || g.sessionPrepared {
		t.Fatal("a supported wall accepted an absent cold-start plan")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("missing settings caused infrastructure writes", err)
	}
}

func TestOffPackageInstallationIsOnceOwnedAndChangedMemberPlanRefuses(t *testing.T) {
	g := &Init{lifetime: t.Context(), packageSettingsRequired: true}
	plan := packageSettingsPlan("off", "7d")
	if err := g.applyBootPackageSettings(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	plan.MinReleaseAge = "1d"
	if g.packageSettingsPlan.MinReleaseAge != "7d" {
		t.Fatal("the actual installed phase borrowed its caller's plan")
	}
	if err := g.checkBootPackageSettings(plan); err == nil {
		t.Fatal("a member replaced the installed cold-start age")
	}
	if err := g.applyBootPackageSettings(t.Context(), plan); err == nil {
		t.Fatal("a retry reran changed package settings")
	}
	original := packageSettingsPlan("off", "7d")
	if err := g.applyBootPackageSettings(t.Context(), original); err != nil {
		t.Fatal("an unchanged prepared retry lost its completion", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.applyBootPackageSettings(ctx, original); !errors.Is(err, context.Canceled) || g.packageSettingsPhase != packageSettingsApplied {
		t.Fatal("an idempotent retry ignored cancellation or changed an installed plan", err)
	}
	if err := g.applyBootPackageSettings(nil, original); err == nil {
		t.Fatal("a matching plan donated a missing runtime lifetime")
	}
	fresh := &Init{packageSettingsRequired: true}
	if err := fresh.applyBootPackageSettings(ctx, original); !errors.Is(err, context.Canceled) || fresh.packageSettingsPhase != packageSettingsUnprepared {
		t.Fatal("cancelled installation occupied or wrote package state", err)
	}
}

func TestActualMemberProjectionUsesHeldPackageDefaultsBeforeProtectedStages(t *testing.T) {
	plan := packageSettingsPlan("strict", "7d")
	// Isolate actual member projection after the helper phase. This metadata
	// fixture proves no UID/helper execution; dedicated runtime tests own that.
	g := &Init{packageSettingsRequired: true, packageSettingsPhase: packageSettingsApplied, packageSettingsPlan: proto.ClonePackageManagersV1(plan)}
	args := proto.CtlStartArgs{PackageManagers: plan, Env: []string{"HOME=/home/agent", "npm_config_ignore_scripts=false", "GOTOOLCHAIN=auto"}}
	if err := g.projectMember(&args, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args.Env, "PIP_ONLY_BINARY=:all:") || !slices.Contains(args.Env, "UV_NO_BUILD=1") || !slices.Contains(args.Env, "npm_config_ignore_scripts=false") || !slices.Contains(args.Env, "GOTOOLCHAIN=auto") {
		t.Fatal("the actual member path lost strict defaults or user precedence")
	}
	clone := cloneMemberArgs(args)
	clone.PackageManagers.StartedAt = "2026-11-16T12:00:00Z"
	if sameMemberIntent(args, clone) || reflect.DeepEqual(inputInfrastructure(args), inputInfrastructure(clone)) {
		t.Fatal("member/input equality ignored a changed installed cutoff")
	}
	if args.PackageManagers.StartedAt != "2026-11-15T12:00:00Z" {
		t.Fatal("the member clone borrowed the private start plan")
	}
}

func TestActualMemberDecoderClosesTheNewPackageSettingsForm(t *testing.T) {
	g := &Init{}
	args := proto.CtlStartArgs{Session: "member234", PackageManagers: packageSettingsPlan("off", "7d")}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.decodeMember(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"package_managers":`, `"PACKAGE_MANAGERS":`, 1),
		strings.Replace(string(raw), `"mode":"off"`, `"mode":"off","Mode":"guarded"`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":2`, 1),
		`{"session":"member234","package_managers":null}`,
		strings.Replace(string(raw), `"v":1`, `"v":1,"path":"/outside"`, 1),
	} {
		if _, err := g.decodeMember([]byte(bad)); err == nil {
			t.Fatal("an alias, future version, null or path entered the actual start decoder")
		}
	}
}

func TestOffPackagePlanRejectsStaleRestoreBeforeHomeWrites(t *testing.T) {
	root := t.TempDir()
	g := &Init{root: root, packageSettingsRequired: true}
	args := proto.CtlStartArgs{PackageManagers: packageSettingsPlan("off", "7d"), StateFiles: []string{".bunfig.toml"}}
	if err := g.prepareSession(&args); err == nil {
		t.Fatal("off restored generated settings from an earlier cold start")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("a stale restore declaration changed the home", err)
	}
}
