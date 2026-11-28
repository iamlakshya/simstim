package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestResolveWallDefaultsAndFloors(t *testing.T) {
	s := DefaultSettings()
	if s.Sandbox.Wall != WallAuto {
		t.Fatalf("DefaultSettings().Sandbox.Wall = %q, want %q", s.Sandbox.Wall, WallAuto)
	}

	// Pinned host floor is used without probing.
	probed := false
	h := Host{Name: "devbox", WallFloor: api.LevelProcess}
	plan, err := ResolveWall(h, s, "", false, func() (wall.Level, error) {
		probed = true
		return wall.MicroVM, nil
	})
	if err != nil {
		t.Fatalf("ResolveWall pinned: %v", err)
	}
	if probed {
		t.Fatal("ResolveWall probed a host with pinned wall_floor")
	}
	if plan.Level != api.LevelProcess || plan.Floor != api.LevelProcess || plan.Downgraded || plan.UnpinnedNotice != "" {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	// Unpinned host probes on start, uses best level without pinning, emits notice.
	hUnpinned := Host{Name: "legacy"}
	plan, err = ResolveWall(hUnpinned, s, "", false, func() (wall.Level, error) {
		return wall.GVisor, nil
	})
	if err != nil {
		t.Fatalf("ResolveWall unpinned: %v", err)
	}
	if plan.Level != api.LevelGVisor || plan.Floor != api.LevelGVisor || plan.Downgraded {
		t.Fatalf("unexpected unpinned plan: %+v", plan)
	}
	if !strings.Contains(plan.UnpinnedNotice, "legacy has no wall floor yet") || !strings.Contains(plan.UnpinnedNotice, "host add legacy") {
		t.Fatalf("UnpinnedNotice = %q", plan.UnpinnedNotice)
	}

	// Explicit --wall plain passes through without probing or floor checks.
	plan, err = ResolveWall(h, s, "plain", false, nil)
	if err != nil || plan.Level != "plain" || plan.Downgraded {
		t.Fatalf("plain plan = %+v, err = %v", plan, err)
	}
}

func TestResolveWallRefusalsAndDowngrades(t *testing.T) {
	s := DefaultSettings()

	// Host with floor=gvisor refuses --wall process without --downgrade (exit 5 / wall_floor).
	h := Host{Name: "devbox", WallFloor: api.LevelGVisor}
	_, err := ResolveWall(h, s, "process", false, nil)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "wall_floor" || ue.Kind != KindRefused {
		t.Fatalf("below floor err = %v, want wall_floor KindRefused", err)
	}
	if !strings.Contains(ue.Msg, "gvisor or stronger") || !strings.Contains(ue.Hint, "--wall process --downgrade") || !strings.Contains(ue.Hint, "host set devbox floor=process") {
		t.Fatalf("unexpected error text: msg=%q hint=%q", ue.Msg, ue.Hint)
	}

	// With --downgrade, it succeeds and records DowngradedFrom = "gvisor".
	plan, err := ResolveWall(h, s, "process", true, nil)
	if err != nil {
		t.Fatalf("downgrade to process: %v", err)
	}
	if plan.Level != api.LevelProcess || !plan.Downgraded || plan.DowngradedFrom != api.LevelGVisor || plan.Floor != api.LevelGVisor {
		t.Fatalf("plan = %+v", plan)
	}

	// Host that can run only none (e.g. Docker) refuses default start without --downgrade (Q6).
	hDocker := Host{Name: "dock", WallFloor: api.LevelNone}
	_, err = ResolveWall(hDocker, s, "", false, nil)
	if !errors.As(err, &ue) || ue.Code != "wall_floor" || ue.Kind != KindRefused {
		t.Fatalf("none-only host err = %v, want wall_floor KindRefused", err)
	}
	if !strings.Contains(ue.Hint, "--wall none --downgrade") {
		t.Fatalf("none-only hint = %q", ue.Hint)
	}

	// Explicit --wall none on a none-floor host STILL needs --downgrade.
	_, err = ResolveWall(hDocker, s, "none", false, nil)
	if !errors.As(err, &ue) || ue.Code != "wall_floor" || ue.Kind != KindRefused {
		t.Fatalf("explicit none without downgrade err = %v, want wall_floor", err)
	}

	// With --downgrade, none-only host succeeds with DowngradedFrom = "default".
	plan, err = ResolveWall(hDocker, s, "none", true, nil)
	if err != nil {
		t.Fatalf("none-only with downgrade: %v", err)
	}
	if plan.Level != api.LevelNone || !plan.Downgraded || plan.DowngradedFrom != "default" {
		t.Fatalf("none-only downgrade plan = %+v", plan)
	}
}

func TestResolveWallSharedHostAndConfigRaising(t *testing.T) {
	s := DefaultSettings()

	// Shared host (e2b machine) never picks process without --downgrade (Q5).
	hE2B := Host{Name: "e2b-box", WallFloor: api.LevelProcess, Machine: &Machine{Provider: "e2b"}}
	if !IsSharedHost(hE2B) {
		t.Fatal("e2b host should be detected as shared")
	}
	_, err := ResolveWall(hE2B, s, "", false, nil)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "wall_floor" || ue.Kind != KindRefused {
		t.Fatalf("shared host process floor err = %v, want wall_floor KindRefused", err)
	}
	if !strings.Contains(ue.Msg, "shared host") || !strings.Contains(ue.Hint, "--wall process --downgrade") {
		t.Fatalf("shared host error: msg=%q hint=%q", ue.Msg, ue.Hint)
	}

	// Shared host with --wall process --downgrade records DowngradedFrom="gvisor".
	plan, err := ResolveWall(hE2B, s, "process", true, nil)
	if err != nil {
		t.Fatalf("shared host process downgrade: %v", err)
	}
	if plan.Level != api.LevelProcess || !plan.Downgraded || plan.DowngradedFrom != api.LevelGVisor {
		t.Fatalf("shared host downgrade plan = %+v", plan)
	}

	// Shared host with floor=gvisor runs at gvisor with no downgrade.
	hE2BG := Host{Name: "e2b-g", WallFloor: api.LevelGVisor, Labels: map[string]string{"tier": "shared"}}
	plan, err = ResolveWall(hE2BG, s, "", false, nil)
	if err != nil || plan.Level != api.LevelGVisor || plan.Downgraded {
		t.Fatalf("shared host gvisor plan = %+v, err = %v", plan, err)
	}

	// Config layer below floor is raised to floor with notice (Q90).
	sLow := DefaultSettings()
	sLow.Sandbox.Wall = api.LevelProcess
	hHigh := Host{Name: "kvmbox", WallFloor: api.LevelMicroVM}
	plan, err = ResolveWall(hHigh, sLow, "", false, nil)
	if err != nil {
		t.Fatalf("config layer below floor: %v", err)
	}
	if plan.Level != api.LevelMicroVM || plan.Downgraded {
		t.Fatalf("plan = %+v, want microvm not downgraded", plan)
	}
	if !strings.Contains(plan.RaisedNotice, "below kvmbox's floor microvm") {
		t.Fatalf("RaisedNotice = %q", plan.RaisedNotice)
	}

	// Config layer above floor raises the target level.
	sHigh := DefaultSettings()
	sHigh.Sandbox.Wall = api.LevelGVisor
	hProc := Host{Name: "vps", WallFloor: api.LevelProcess}
	plan, err = ResolveWall(hProc, sHigh, "", false, nil)
	if err != nil || plan.Level != api.LevelGVisor || plan.RaisedNotice != "" {
		t.Fatalf("config layer above floor plan = %+v, err = %v", plan, err)
	}

	// Host tiers.toml support.
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	appDir := ConfigDir()
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "tiers.toml"), []byte("tenantbox = \"shared\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsSharedHost(Host{Name: "tenantbox"}) {
		t.Fatal("tiers.toml shared host was not detected")
	}
}

func TestUnwalledShellArgsPropagatesDowngrade(t *testing.T) {
	t.Run("reuses target wall floor and downgraded_from", func(t *testing.T) {
		h := Host{Name: "devbox", WallFloor: api.LevelProcess}
		target := Session{
			ID:  "s1",
			Cwd: "/srv/app",
			Wall: &api.WallInfo{
				Level:          api.LevelNone,
				Driver:         api.LevelNone,
				Floor:          api.LevelGVisor,
				DowngradedFrom: api.LevelGVisor,
			},
		}
		got := UnwalledShellArgs(h, target, ShellOptions{Name: "sh1", Cols: 80, Rows: 24, Settings: DefaultSettings()})
		if got.Agent != "shell" || got.Cwd != "/srv/app" || got.Name != "sh1" {
			t.Fatalf("unexpected base args: %+v", got)
		}
		if got.Wall != api.LevelNone || !got.Downgrade || got.WallFloor != api.LevelGVisor || got.DowngradedFrom != api.LevelGVisor {
			t.Fatalf("downgrade args = %+v, want wall=none downgrade=true floor=gvisor from=gvisor", got)
		}
	})

	t.Run("falls back to host floor when target has no wall info", func(t *testing.T) {
		h := Host{Name: "devbox", WallFloor: api.LevelProcess}
		target := Session{ID: "s2", Cwd: "/srv/app"}
		got := UnwalledShellArgs(h, target, ShellOptions{Cols: 80, Rows: 24, Settings: DefaultSettings()})
		if got.Wall != api.LevelNone || !got.Downgrade || got.WallFloor != api.LevelProcess || got.DowngradedFrom != api.LevelProcess {
			t.Fatalf("downgrade args = %+v, want wall=none downgrade=true floor=process from=process", got)
		}
	})

	t.Run("shared host downgrades from gvisor", func(t *testing.T) {
		h := Host{Name: "e2b-box", WallFloor: api.LevelProcess, Machine: &Machine{Provider: "e2b", ID: "sbx1"}}
		target := Session{ID: "s3", Cwd: "/srv/app"}
		got := UnwalledShellArgs(h, target, ShellOptions{Cols: 80, Rows: 24, Settings: DefaultSettings()})
		if got.Wall != api.LevelNone || !got.Downgrade || got.WallFloor != api.LevelProcess || got.DowngradedFrom != api.LevelGVisor {
			t.Fatalf("shared downgrade args = %+v, want floor=process from=gvisor", got)
		}
	})
}
