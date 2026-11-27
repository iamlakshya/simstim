package host

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

// openStoppedCopyRouteV1 retains one actual native stopped reader and SAME
// primary OP for the entire original list/range/EOF connection. It never
// starts an agent or installs fresh named keys, secrets or egress.
func openStoppedCopyRouteV1(ctx context.Context, id string) (_ *bridgeCopyRoute, returned error) {
	operation, err := BeginSessionOperation(ctx, id, 5*time.Second)
	if err != nil {
		return nil, err
	}
	route := &bridgeCopyRoute{session: id, operation: operation}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, route.close())
		}
	}()
	saved, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if saved.Wall == nil || saved.Wall.Level == "none" {
		return nil, UserError("cp_unwalled", "the selected session runs without a wall", "Use scp for that host's own files.")
	}
	limit, err := filecopy.CopyMax(saved.Policy)
	if err != nil {
		return nil, err
	}
	route.guest, err = OpenStoppedGuest(ctx, operation)
	if err != nil {
		return nil, err
	}
	route.reader, err = filecopy.NewReadSessionV1(id, limit, func(ctx context.Context, header proto.FileGetHeaderV1) ([]byte, error) {
		return filecopy.ReadGuestV1(ctx, route.guest.Open, header)
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	complete = true
	return route, nil
}
