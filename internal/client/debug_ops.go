package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// DebugState is the answer to debug start and debug stop.
type DebugState = api.DebugState

// DebugFileInfo names a file that the host wrote, and its size.
type DebugFileInfo = api.DebugFile

// needDebug fails, saying what to do, when the host's helper predates the
// debug mode.
func (c *Conn) needDebug() error {
	if c.HasFeature(api.FeatureDebug) {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("the host helper on %s is too old for the debug mode", c.Host.Name),
		Hint: brand.Name + " host add " + c.Host.Name + "   (updates the helper)",
	}
}

// callDebug makes one debug request about a session, after checking that
// the helper serves it. A session that started before the helper's update
// keeps its old holder, which does not know the request.
func (c *Conn) callDebug(ctx context.Context, op string, args, out any) error {
	if err := c.needDebug(); err != nil {
		return err
	}
	err := c.Call(ctx, op, args, out)
	var ue *UserError
	if errors.As(err, &ue) && ue.Code == "unknown_op" {
		return &UserError{
			Msg:  "this session started before the helper on " + c.Host.Name + " was updated, so it has no debug mode yet",
			Hint: "Debug works once the session starts again, for example after the host restarts.",
		}
	}
	return err
}

// DebugStart turns on the debug of a session, with the host categories
// cats, for d: api.DebugFor when 0.
func (c *Conn) DebugStart(ctx context.Context, id string, cats diag.Cats, d time.Duration) (DebugState, error) {
	var st DebugState
	err := c.callDebug(ctx, api.OpDebugStart, api.DebugStartArgs{ID: id, Categories: cats.List(), Seconds: int(d / time.Second)}, &st)
	return st, err
}

// DebugStop ends the debug of a session at once, with the host side of each
// traced attach.
func (c *Conn) DebugStop(ctx context.Context, id string) (DebugState, error) {
	var st DebugState
	err := c.callDebug(ctx, api.OpDebugStop, api.DebugStartArgs{ID: id}, &st)
	return st, err
}

// DebugProfile asks a session's holder for a profile: cpu for d, heap or
// goroutine. The holder writes it in the session's debug folder.
func (c *Conn) DebugProfile(ctx context.Context, id, kind string, d time.Duration) (DebugFileInfo, error) {
	var f DebugFileInfo
	err := c.callDebug(ctx, api.OpDebugProfile, api.DebugProfileArgs{ID: id, Kind: kind, Seconds: int(d / time.Second)}, &f)
	return f, err
}

// DebugSave asks a session's holder to save the window of its flight
// recorder.
func (c *Conn) DebugSave(ctx context.Context, id string) (DebugFileInfo, error) {
	var f DebugFileInfo
	err := c.callDebug(ctx, api.OpDebugSave, api.DebugStartArgs{ID: id}, &f)
	return f, err
}
