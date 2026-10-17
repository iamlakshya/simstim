package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
)

// The requests of the debug bundle (decision 0025), and the debug of one
// connection's bridge, which the launcher turns on and off.

// DebugCollected is the host's part of a bundle.
type DebugCollected = api.DebugCollected

// DebugCollect asks the host for its part of a bundle: the files of the
// sessions ids, or with all, of every session that has debug files.
func (c *Conn) DebugCollect(ctx context.Context, ids []string, all bool) (DebugCollected, error) {
	var out DebugCollected
	err := c.callBundle(ctx, api.OpDebugCollect, api.DebugCollectArgs{IDs: ids, All: all}, &out)
	return out, err
}

// DebugReadFile reads a file that DebugCollect listed, part by part, up to
// the size that it had then: a file that a process still writes grows. It
// returns the file, with the alerts file's values replaced on the host,
// and how many the host replaced. It fails beyond limit bytes.
func (c *Conn) DebugReadFile(ctx context.Context, f DebugFileInfo, limit int64) ([]byte, int, error) {
	var buf bytes.Buffer
	hidden := 0
	var off int64
	for {
		var ch api.DebugChunk
		if err := c.callBundle(ctx, api.OpDebugRead, api.DebugReadArgs{Name: f.Path, Offset: off}, &ch); err != nil {
			return nil, 0, err
		}
		buf.Write(ch.Data)
		hidden += ch.Hidden
		if int64(buf.Len()) > limit {
			return nil, 0, fmt.Errorf("%s is larger than %d MiB", f.Path, limit>>20)
		}
		if ch.EOF || ch.Next >= f.Size || ch.Next <= off {
			return buf.Bytes(), hidden, nil
		}
		off = ch.Next
	}
}

// callBundle makes one request of the bundle. A helper from before the
// bundle does not know it.
func (c *Conn) callBundle(ctx context.Context, op string, args, out any) error {
	if err := c.needDebug(); err != nil {
		return err
	}
	err := c.Call(ctx, op, args, out)
	var ue *UserError
	if errors.As(err, &ue) && ue.Code == "unknown_op" {
		return &UserError{
			Msg:  fmt.Sprintf("the host helper on %s is too old for the debug bundle", c.Host.Name),
			Hint: brand.Name + " host add " + c.Host.Name + "   (updates the helper)",
		}
	}
	return err
}

// StartConnDebug asks this connection's bridge to record its side, with the
// categories of cats that a bridge records, as a client whose debug is on
// does at each dial.
func (c *Conn) StartConnDebug(ctx context.Context, cats diag.Cats) (DebugState, error) {
	var st DebugState
	if err := c.needDebug(); err != nil {
		return st, err
	}
	err := c.Call(ctx, api.OpDebugStart, api.DebugStartArgs{Categories: (cats & diag.BridgeCats).List()}, &st)
	return st, err
}

// StopConnDebug ends the debug of this connection's bridge.
func (c *Conn) StopConnDebug(ctx context.Context) error {
	if err := c.needDebug(); err != nil {
		return err
	}
	return c.Call(ctx, api.OpDebugStop, api.DebugStartArgs{}, nil)
}

// SaveDebugFlight saves the window of the client's own flight recorder,
// which runs while the client's runtime category is on, and returns the
// file's path.
func SaveDebugFlight() (string, error) {
	p, err := debugRuntime().Save("request")
	if errors.Is(err, diag.ErrNoRecorder) {
		return "", &UserError{Msg: "the flight recorder of this process is off: it runs while the runtime category is on",
			Hint: "Start debug with every category, or with --debug=runtime."}
	}
	if err != nil {
		return "", fmt.Errorf("save the flight recorder: %w", err)
	}
	return p, nil
}
