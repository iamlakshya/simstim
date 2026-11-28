package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/wall"
)

func parseWallFlag(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	w := strings.ToLower(strings.TrimSpace(raw))
	switch w {
	case client.WallAuto, api.LevelNone, api.LevelProcess, api.LevelGVisor, api.LevelMicroVM, "plain":
		return w, nil
	default:
		return "", &client.UserError{
			Msg:  fmt.Sprintf("--wall: unknown wall level %q", raw),
			Hint: "Pass auto, process, gvisor, microvm or none.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
}

func probeConnWallFloor(ctx context.Context, conn *client.Conn) (wall.Level, error) {
	if conn == nil || !conn.HasFeature("sandbox") {
		return wall.None, nil
	}
	p, err := conn.CapProbe(ctx, "")
	if err != nil {
		return wall.None, err
	}
	nowName, _ := api.LevelsFor(p, api.RuntimeStatus{})
	lvl, _ := wall.ParseLevel(nowName)
	return lvl, nil
}

// applyNoneDowngrade populates Wall, Downgrade, WallFloor and DowngradedFrom on
// an intentionally unwalled session (agents install / agents login) so the
// trusted manifest records the downgrade, rows show !, and wall_downgraded
// fires without requiring --downgrade (cli-spec 12, isolation spec Q97).
func applyNoneDowngrade(ctx context.Context, conn *client.Conn, h client.Host, a *client.NewArgs) {
	a.Wall = api.LevelNone
	a.Downgrade = true
	var probed wall.Level
	if strings.TrimSpace(h.WallFloor) == "" && conn != nil {
		if lvl, err := probeConnWallFloor(ctx, conn); err == nil {
			probed = lvl
		}
	}
	a.WallFloor, a.DowngradedFrom = client.DowngradeForNone(h, probed)
}

func printWallNotices(cmd *cobra.Command, plan client.WallPlan) {
	if plan.UnpinnedNotice != "" {
		note(cmd, "wall_unpinned", plan.UnpinnedNotice)
	}
	if plan.RaisedNotice != "" {
		note(cmd, "wall_raised", plan.RaisedNotice)
	}
}
