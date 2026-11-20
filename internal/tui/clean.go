package tui

import (
	"context"

	"github.com/iamlakshya/simstim/internal/client"
)

// What a host sends is not trusted, and the launcher draws it cleaned
// (api.CleanText). Cleaning changes some text, so the model keeps each
// session row as the host sent it, for the requests that go back: its ID,
// its name, its folder and its secrets' names. Each place that draws a row
// draws shown(row) instead. Toasts and errors are cleaned where they are
// drawn (toastStack.push, errText).

// shown is a copy of a session row to draw, with every string that the
// host sent cleaned.
func shown(s client.Session) client.Session { return client.CleanSession(s) }

// cleanConn cleans, on a connection, the answers that the launcher only
// draws: the host's doctor report, its setup steps and its agent checks.
// It offers only the folder suggestions that cleaning leaves as they are,
// because a suggestion goes back to the host as a session's folder. Session
// rows pass as the host sent them. A peek's bytes pass too: the preview
// draws them through its own terminal model, and cleans its cells.
type cleanConn struct{ Conn }

// dialClean dials h through be, and cleans what the host sends on the
// connection.
func dialClean(ctx context.Context, be Backend, h client.Host) (Conn, error) {
	c, err := be.Dial(ctx, h)
	if err != nil || c == nil {
		return c, err
	}
	return cleanConn{c}, nil
}

// unclean is the connection under c, for the debug requests, which are
// not part of Conn.
func unclean(c Conn) Conn {
	if cc, ok := c.(cleanConn); ok {
		return cc.Conn
	}
	return c
}

func (c cleanConn) Doctor(ctx context.Context) (client.HostInfo, error) {
	info, err := c.Conn.Doctor(ctx)
	return client.CleanHostInfo(info), err
}

func (c cleanConn) Setup(ctx context.Context) (client.SetupResult, error) {
	r, err := c.Conn.Setup(ctx)
	r.Steps = client.CleanChecks(r.Steps)
	return r, err
}

func (c cleanConn) Folders(ctx context.Context, prefix string) (client.FoldersResult, error) {
	r, err := c.Conn.Folders(ctx, prefix)
	return client.OfferFolders(r), err
}

func (c cleanConn) AgentChecks(ctx context.Context) ([]client.AgentCheck, error) {
	list, err := c.Conn.AgentChecks(ctx)
	return client.CleanAgentChecks(list), err
}
