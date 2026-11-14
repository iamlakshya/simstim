package client

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

// WallChoice is one start's selected level and display metadata. The floor
// remains the actual saved floor even on a host below its tier's minimum.
type WallChoice struct {
	Level          string
	Floor          string
	DowngradedFrom string
}

// WallFloorError carries the exact named level for the launcher's downgrade
// sheet while exposing the canonical user error to the CLI and JSON output.
type WallFloorError struct {
	Level string
	Floor string
	Err   *UserError
}

// Error returns the underlying user-facing message.
func (e *WallFloorError) Error() string { return e.Err.Error() }

// Unwrap makes the canonical error code, kind and hint available to callers.
func (e *WallFloorError) Unwrap() error { return e.Err }

// ValidateWallRequest checks only the named level and downgrade flag. It does
// not read host policy or infer which wall a host can run.
func ValidateWallRequest(request string, downgrade bool) error {
	if downgrade && (request == "" || request == "auto") {
		return namedDowngradeError()
	}
	if request == "" || request == "auto" {
		return nil
	}
	if _, err := wall.ParseLevel(request); err != nil {
		return &UserError{Msg: fmt.Sprintf("%q is not a wall level", CleanLine(request)),
			Hint: "Use --wall auto, none, process, gvisor or microvm.", Code: "bad_floor", Kind: KindUsage}
	}
	return nil
}

// ChooseWall resolves an explicit level or auto, which means the saved floor.
// An empty request preserves the legacy SDK path. Front ends explicitly select auto. A named
// downgrade waives only the canonical wall_floor refusal for this start.
func ChooseWall(h Host, tier Tier, request string, downgrade bool) (WallChoice, error) {
	if err := ValidateWallRequest(request, downgrade); err != nil {
		return WallChoice{}, err
	}
	if request == "" {
		return WallChoice{Level: "none"}, nil
	}
	saved := h.WallFloor
	if saved == "" {
		saved = "none"
	}
	floor, err := ParseWallFloor(saved)
	if err != nil {
		return WallChoice{}, err
	}
	level := floor
	if request != "auto" {
		level, err = wall.ParseLevel(request)
		if err != nil {
			return WallChoice{}, &UserError{Msg: fmt.Sprintf("%q is not a wall level", CleanLine(request)),
				Hint: "Use --wall auto, none, process, gvisor or microvm.", Code: "bad_floor", Kind: KindUsage}
		}
	}
	choice := WallChoice{Level: level.String(), Floor: floor.String()}
	gate := allowFloor(h, tier, Outbound{Kind: OutboundFloor, Name: choice.Level})
	if gate != nil {
		if !downgrade {
			return WallChoice{}, wallBelowFloor(h, choice.Level, MinimumWallFloor(tier))
		}
		choice.DowngradedFrom = MinimumWallFloor(tier)
	}
	if level < floor {
		if !downgrade {
			return WallChoice{}, wallBelowFloor(h, choice.Level, floor.String())
		}
		if old, _ := wall.ParseLevel(choice.DowngradedFrom); old < floor {
			choice.DowngradedFrom = floor.String()
		}
	}
	return choice, nil
}

func namedDowngradeError() error {
	return &UserError{Msg: "--downgrade needs an explicit wall level",
		Hint: "Name the level with --wall none, process, gvisor or microvm.", Code: "flags_conflict", Kind: KindUsage}
}

func wallBelowFloor(h Host, level, floor string) error {
	return &WallFloorError{Level: level, Floor: floor, Err: &UserError{Msg: fmt.Sprintf("%s runs sessions at %s or stronger, and %s is weaker", hostDisplayName(h), floor, level),
		Hint: brand.Name + " new " + hostCommandArg(h) + " --wall " + level + " --downgrade",
		Code: "wall_floor", Kind: KindRefused}}
}
