package client

import "context"

// The ordinary start path checks the concrete choice against the accepted
// connection's saved floor and the current tier. Exported argument fields do
// not establish a downgrade. The original settings and transport observations
// are checked again by the existing physical writer.
func (c *Conn) checkNewWallChoiceV1(ctx context.Context, a NewArgs) error {
	if a.Wall == "" || a.Wall == "auto" {
		return newWallChoiceErrorV1()
	}
	tier, err := c.checkWallSelectionSource(ctx, a.Wall)
	if err != nil {
		return err
	}
	choice, err := ChooseWall(c.Host, tier, a.Wall, a.Downgrade)
	if err != nil {
		return err
	}
	if choice.Level != a.Wall || a.Floor != c.Host.WallFloor || choice.DowngradedFrom != a.DowngradedFrom {
		return newWallChoiceErrorV1()
	}
	return ctx.Err()
}

func newWallChoiceErrorV1() error {
	return &UserError{Msg: "this start has no matching resolved wall choice", Hint: "Resolve the current host floor before preparing the start and its secrets.", Code: "wall_intent", Kind: KindRefused}
}
