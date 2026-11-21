package client

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// ResolveNewWall selects a concrete wall before commands fetch secret values.
// A fresh probe cannot select another saved host or refresh an old request's
// Source. The shared publication participant is released while the probe runs.
func (c *Conn) ResolveNewWall(ctx context.Context, a NewArgs) (NewArgs, error) {
	if err := checkSessionMachineNewV1(ctx, c); err != nil {
		return a, err
	}
	if err := ValidateWallRequest(a.Wall, a.Downgrade); err != nil {
		return a, err
	}
	var operation *settingsOperation
	var err error
	if request := grantRequestOf(ctx); request != nil {
		operation, err = request.borrowNewSettings(ctx)
	} else {
		operation, err = openSettingsOperation(ctx)
		if err == nil {
			defer operation.Close()
		}
	}
	if err != nil {
		return a, err
	}
	if _, err := operation.Settings(ctx); err != nil {
		return a, err
	}
	ctx, _ = ensureGrantRequest(ctx)
	tier, err := c.checkWallSelectionSource(ctx, a.Wall)
	if err != nil {
		return a, err
	}
	requested := a.Wall
	if requested == "" {
		requested = "auto"
	}
	host := cloneGrantHost(c.Host)
	if requested == "auto" && host.WallFloor == "" {
		return a, missingPinnedWallFloor(host)
	}
	var probe api.CapProbe
	if requested != "none" {
		probe, err = c.CapProbe(ctx, "")
		if err != nil {
			return a, err
		}
	}
	choice, err := ChooseWall(host, tier, requested, a.Downgrade)
	if err != nil {
		return a, err
	}
	if choice.Level != "none" {
		if err := c.checkNewWallConstruction(choice.Level, probe); err != nil {
			return a, err
		}
	}
	if _, err := c.checkWallSelectionSource(ctx, choice.Level); err != nil {
		return a, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return a, err
	}
	a.Wall = choice.Level
	a.Floor = c.Host.WallFloor
	a.DowngradedFrom = choice.DowngradedFrom
	return a, nil
}

func (c *Conn) checkWallSelectionSource(ctx context.Context, level string) (Tier, error) {
	ctx, release, err := c.beginGrant(ctx, "select wall", false)
	if err != nil {
		return TierUnknown, err
	}
	defer release()
	// Ordinary legacy None bookkeeping remains the only unchecked route.
	if level != "" && level != "none" && !ValidHostID(c.HostID()) {
		return TierUnknown, grantIdentityError(c.Host)
	}
	tiers, err := readTiersFile()
	if err != nil {
		return TierUnknown, err
	}
	entry, ok := tiers.Entry(c.Host.Name)
	if !ok || entry.HostID != c.HostID() {
		return TierUnknown, grantIdentityError(c.Host)
	}
	return min(tiers.Of(c.Host.Name), requestCeiling(ctx, tiers.Of(c.Host.Name))), nil
}

// A locally chosen gVisor level may reach its actual pre-value runtime
// preparation from verified construction facts. This is not physical OK:
// prepareNewRuntime requires its private post-delivery probe before values.
// Display/BestWallLevel, stored floors and the final runnable check stay exact.
func (c *Conn) checkNewWallConstruction(level string, probe api.CapProbe) error {
	if level == "gvisor" {
		for _, candidate := range probe.Walls {
			if candidate.Driver == "gvisor" && candidate.Level == level && candidate.CanPrepare {
				return nil
			}
		}
	}
	return c.checkRunnableWall(level, probe)
}

func (c *Conn) checkRunnableWall(level string, probe api.CapProbe) error {
	if level == "process" && !c.HasFeature("wall.process") {
		return c.grantHelperOld("run the process wall")
	}
	for _, candidate := range probe.Walls {
		if candidate.Level == level && candidate.OK {
			return nil
		}
	}
	return &UserError{Msg: fmt.Sprintf("%s cannot currently run the %s wall", hostDisplayName(c.Host), level),
		Hint: brand.Name + " host prepare " + hostCommandArg(c.Host), Code: "wall_unavailable", Kind: KindRefused}
}
