package client

import (
	"fmt"
	"path/filepath"
	"strings"

	tomlv2 "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

// WallAuto asks new sessions to run at the host's floor (or the strongest level
// probed on a host without a saved floor).
const WallAuto = "auto"

// WallPlan is the resolved sandbox wall for a new session (isolation spec 6.1,
// 8.2 rule 5, 8.4, 9.3, Q5, Q6, Q90).
type WallPlan struct {
	Level          string
	Floor          string
	Downgraded     bool
	DowngradedFrom string
	UnpinnedNotice string
	RaisedNotice   string
}

// ParseWallSetting validates a sandbox.wall value ("auto", "none", "process",
// "gvisor" or "microvm") and returns it normalised to lowercase.
func ParseWallSetting(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || v == WallAuto {
		return WallAuto, nil
	}
	lvl, err := wall.ParseLevel(v)
	if err != nil {
		return "", &UserError{
			Msg:  fmt.Sprintf("unknown wall level %q", raw),
			Hint: `Use "auto", "none", "process", "gvisor" or "microvm".`,
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	return lvl.String(), nil
}

// IsSharedHost reports whether h belongs to the "shared" trust tier (spec 14.8,
// Q5, Q32), where `new` never picks `process` without `--downgrade`.
func IsSharedHost(h Host) bool {
	if h.Machine != nil && strings.EqualFold(strings.TrimSpace(h.Machine.Provider), "e2b") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(h.Labels["tier"]), "shared") {
		return true
	}
	return strings.EqualFold(loadHostTier(h.Name), "shared")
}

// loadHostTier reads an optional tiers.toml in ConfigDir (H4 forward-compat)
// and returns the saved tier for hostName, or "" when absent or unreadable.
func loadHostTier(hostName string) string {
	if strings.TrimSpace(hostName) == "" {
		return ""
	}
	b, exists, err := ReadConfigFile(filepath.Join(ConfigDir(), "tiers.toml"))
	if err != nil || !exists {
		return ""
	}
	var raw map[string]any
	if err := tomlv2.Unmarshal(b, &raw); err != nil {
		return ""
	}
	if tier := tierFromEntry(raw[hostName]); tier != "" {
		return tier
	}
	if hosts, ok := raw["hosts"].(map[string]any); ok {
		if tier := tierFromEntry(hosts[hostName]); tier != "" {
			return tier
		}
	}
	if list, ok := raw["host"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := m["name"].(string); name == hostName {
				if tier, _ := m["tier"].(string); tier != "" {
					return strings.TrimSpace(tier)
				}
			}
		}
	}
	return ""
}

func tierFromEntry(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		if t, ok := x["tier"].(string); ok {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

// ResolveWall decides the wall level for a new session on host h given
// settings s, optional --wall flag flagWall, --downgrade, and a lazy probe
// callback invoked only when h.WallFloor is empty.
//
// Rules (isolation spec 6.1, 8.2 rule 5, 8.4, 9.3, Q5, Q6, Q90):
//   - "--wall plain" passes through as the test-only supervisor driver.
//   - A host with wall_floor uses it as the floor without probing.
//   - A host without wall_floor probes on start, uses the strongest level found
//     without pinning, and returns UnpinnedNotice pointing to host add / host set.
//   - On a shared host, the minimum level without --downgrade is gvisor (Q5).
//   - A config layer below the host's floor is raised to the floor with
//     RaisedNotice (Q90); a config layer above the floor raises the target.
//   - Running at "none" or below the minimum level requires downgrade; without
//     it, ResolveWall returns a UserError with code "wall_floor" (KindRefused).
func ResolveWall(h Host, s Settings, flagWall string, downgrade bool, probe func() (wall.Level, error)) (WallPlan, error) {
	flag := strings.ToLower(strings.TrimSpace(flagWall))
	if flag == "plain" {
		return WallPlan{Level: "plain"}, nil
	}
	var explicitLevel wall.Level
	hasExplicit := false
	if flag != "" && flag != WallAuto {
		lvl, err := wall.ParseLevel(flag)
		if err != nil {
			return WallPlan{}, &UserError{
				Msg:  fmt.Sprintf("unknown wall %q", flagWall),
				Hint: "Use --wall auto, --wall process, --wall gvisor, --wall microvm, or --wall none.",
				Code: "bad_value",
				Kind: KindUsage,
			}
		}
		explicitLevel = lvl
		hasExplicit = true
	}

	cfgLevel, cfgSource, hasCfg, err := resolveConfigWall(s, h)
	if err != nil {
		return WallPlan{}, err
	}

	var floor wall.Level
	var unpinnedNotice string
	if rawFloor := strings.TrimSpace(h.WallFloor); rawFloor != "" {
		floor, err = wall.ParseLevel(rawFloor)
		if err != nil {
			return WallPlan{}, &UserError{
				Msg:  fmt.Sprintf("host %s has invalid wall_floor %q", h.Name, h.WallFloor),
				Hint: fmt.Sprintf("%s config check, or %s host set %s floor=process", brand.Name, brand.Name, h.Name),
				Code: "bad_value",
				Kind: KindUsage,
			}
		}
	} else {
		if probe == nil {
			return WallPlan{}, &UserError{
				Msg:  "cannot probe wall support on " + h.Name,
				Hint: fmt.Sprintf("%s host add %s, or %s host set %s floor=process", brand.Name, h.Name, brand.Name, h.Name),
				Code: "wall_probe_failed",
				Kind: KindFailed,
			}
		}
		floor, err = probe()
		if err != nil {
			return WallPlan{}, err
		}
		unpinnedNotice = formatUnpinnedNotice(h.Name, floor)
	}

	shared := IsSharedHost(h)
	minLevel := floor
	if shared && minLevel < wall.GVisor {
		minLevel = wall.GVisor
	}

	plan := WallPlan{
		Floor:          floor.String(),
		UnpinnedNotice: unpinnedNotice,
	}

	var target wall.Level
	if hasExplicit {
		target = explicitLevel
	} else {
		target = floor
		if hasCfg {
			switch {
			case cfgLevel > target:
				target = cfgLevel
			case cfgLevel < floor:
				plan.RaisedNotice = fmt.Sprintf("sandbox.wall = %q in %s is below %s's floor %s; using %s", cfgLevel, cfgSource, h.Name, floor, floor)
			}
		}
	}

	needsDowngrade := target < minLevel || target == wall.None
	if needsDowngrade {
		if !downgrade {
			return WallPlan{}, wallFloorRefused(h.Name, shared, floor, minLevel, target)
		}
		from := minLevel.String()
		if minLevel == wall.None {
			from = "default"
		}
		plan.Level = target.String()
		plan.Downgraded = true
		plan.DowngradedFrom = from
		return plan, nil
	}

	plan.Level = target.String()
	return plan, nil
}

func resolveConfigWall(s Settings, h Host) (lvl wall.Level, source string, has bool, err error) {
	if raw := strings.ToLower(strings.TrimSpace(s.Sandbox.Wall)); raw != "" && raw != WallAuto {
		l, perr := wall.ParseLevel(raw)
		if perr != nil {
			return wall.None, "", false, &UserError{
				Msg:  fmt.Sprintf("sandbox.wall in config.toml must be \"auto\", \"none\", \"process\", \"gvisor\" or \"microvm\", not %q", s.Sandbox.Wall),
				Hint: brand.Name + " config check",
				Code: "bad_value",
				Kind: KindUsage,
			}
		}
		lvl, source, has = l, "config.toml", true
	}
	if h.Sandbox != nil {
		if raw := strings.ToLower(strings.TrimSpace(h.Sandbox.Wall)); raw != "" && raw != WallAuto {
			l, perr := wall.ParseLevel(raw)
			if perr != nil {
				return wall.None, "", false, &UserError{
					Msg:  fmt.Sprintf("host %s sandbox.wall in hosts.toml must be \"auto\", \"none\", \"process\", \"gvisor\" or \"microvm\", not %q", h.Name, h.Sandbox.Wall),
					Hint: brand.Name + " config check",
					Code: "bad_value",
					Kind: KindUsage,
				}
			}
			// Layers can raise the level, never lower a stronger layer.
			if !has || l > lvl {
				lvl, source, has = l, "hosts.toml", true
			}
		}
	}
	return lvl, source, has, nil
}

func formatUnpinnedNotice(hostName string, floor wall.Level) string {
	if floor > wall.None {
		return fmt.Sprintf("%s has no wall floor yet; using %s without pinning. Try: %s host add %s (or %s host set %s floor=%s)",
			hostName, floor, brand.Name, hostName, brand.Name, hostName, floor)
	}
	return fmt.Sprintf("%s has no wall floor yet. Try: %s host add %s (or %s host set %s floor=none)",
		hostName, brand.Name, hostName, brand.Name, hostName)
}

func wallFloorRefused(hostName string, shared bool, floor, minLevel, want wall.Level) *UserError {
	if shared && want < wall.GVisor {
		msg := fmt.Sprintf("%s is a shared host, which runs sessions at %s or stronger, and %s is weaker", hostName, minLevel, want)
		if want == wall.None && floor == wall.None {
			msg = fmt.Sprintf("%s is a shared host and can run only wall %q; running without a sandbox needs --downgrade", hostName, api.LevelNone)
		}
		return &UserError{
			Msg:  msg,
			Hint: fmt.Sprintf("%s new %s --wall %s --downgrade (records a downgrade)", brand.Name, hostName, want),
			Code: "wall_floor",
			Kind: KindRefused,
		}
	}
	if want == wall.None && floor == wall.None {
		return &UserError{
			Msg:  fmt.Sprintf("%s can run only wall %q; running without a sandbox needs --downgrade", hostName, api.LevelNone),
			Hint: fmt.Sprintf("%s new %s --wall none --downgrade (records a downgrade), or use another host", brand.Name, hostName),
			Code: "wall_floor",
			Kind: KindRefused,
		}
	}
	hint := fmt.Sprintf("%s new %s --wall %s --downgrade (records a downgrade)", brand.Name, hostName, want)
	if want != wall.None {
		hint += fmt.Sprintf(", or %s host set %s floor=%s", brand.Name, hostName, want)
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s runs sessions at %s or stronger, and %s is weaker", hostName, floor, want),
		Hint: hint,
		Code: "wall_floor",
		Kind: KindRefused,
	}
}

// DowngradeForNone returns the WallFloor and DowngradedFrom metadata for a
// command that intentionally starts an unwalled ("none") session on host h
// without requiring --downgrade, such as `agents install` or `agents login`
// (CLI spec 12, isolation spec Q97).
func DowngradeForNone(h Host, probedFloor wall.Level) (floor, downgradedFrom string) {
	f := probedFloor
	if raw := strings.TrimSpace(h.WallFloor); raw != "" {
		if parsed, err := wall.ParseLevel(raw); err == nil {
			f = parsed
		}
	}
	floor = f.String()
	minLevel := f
	if IsSharedHost(h) && minLevel < wall.GVisor {
		minLevel = wall.GVisor
	}
	if minLevel == wall.None {
		downgradedFrom = "default"
	} else {
		downgradedFrom = minLevel.String()
	}
	return floor, downgradedFrom
}

// WallDowngradeLosses returns human descriptions of what is lost when running
// below floor (for the launcher downgrade confirmation sheet, isolation spec 9.4).
func WallDowngradeLosses(from, to string) []string {
	var out []string
	switch strings.ToLower(strings.TrimSpace(to)) {
	case api.LevelNone:
		out = append(out,
			"No sandbox wall: the agent runs directly on the host with your user's permissions.",
			"No filesystem mount boundary: your home folder and SSH keys are readable.",
			"No network egress policy or secret gateway isolation.",
		)
	case api.LevelProcess:
		out = append(out,
			"Runs on the host kernel with bubblewrap namespaces instead of "+from+".",
			"Shares the host kernel rather than an isolated user-space or microVM kernel.",
		)
	case api.LevelGVisor:
		out = append(out,
			"Runs under gVisor user-space kernel instead of a hardware microVM.",
		)
	}
	out = append(out, "The downgrade is recorded in the manifest, marked with !, and fires a wall_downgraded alert.")
	return out
}
