package client

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Session is a session row, as the host reports it.
type Session = api.SessionInfo

// HostInfo is the host doctor's report.
type HostInfo = api.HostInfo

// NewArgs describes a new session.
type NewArgs = api.NewArgs

// TerminalArgs fills the terminal identity of a new session from this terminal.
func TerminalArgs(a NewArgs, cols, rows int) NewArgs {
	a.Term = os.Getenv("TERM")
	a.ColorTerm = os.Getenv("COLORTERM")
	a.TermProgram = os.Getenv("TERM_PROGRAM")
	a.TermProgramVersion = os.Getenv("TERM_PROGRAM_VERSION")
	a.Cols, a.Rows = cols, rows
	a.Inline = a.Inline || LoadSettings().Agents.Inline
	return a
}

// Sessions lists a host's sessions.
func (c *Conn) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	err := c.Call(ctx, "ls", nil, &out)
	return out, err
}

// NewSession starts a session. Unless a names a memory cap, the session gets
// the one in config.toml.
func (c *Conn) NewSession(ctx context.Context, a NewArgs) (*Session, error) {
	a, err := withMemoryMax(a)
	if err != nil {
		return nil, err
	}
	if err := c.RequireSecrets(a); err != nil {
		return nil, err
	}
	if err := c.needMakeFolder(a); err != nil {
		return nil, err
	}
	var s Session
	if err := c.Call(ctx, "new", a, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// needMakeFolder fails when a start asks the host's helper to make its folder,
// and the helper is too old to do that. An older helper ignores the request.
func (c *Conn) needMakeFolder(a NewArgs) error {
	if !a.MakeFolder || c.HasFeature("mkdir") {
		return nil
	}
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old to make folders", Hint: brand.Name + " host add " + c.Host.Name + "   (updates the helper)"}
}

// DeleteResult says what deleting a session did.
type DeleteResult = api.DeleteResult

// DeleteSession stops and removes a session. The result says what happened to
// its worktree, if it had one.
func (c *Conn) DeleteSession(ctx context.Context, id string) error {
	_, err := c.Delete(ctx, id)
	return err
}

// Delete stops and removes a session, and reports what happened to its worktree.
func (c *Conn) Delete(ctx context.Context, id string) (DeleteResult, error) {
	var r DeleteResult
	err := c.Call(ctx, "delete", map[string]string{"id": id}, &r)
	return r, err
}

// Port is a TCP port a session listens on.
type Port = api.Port

// Ports lists the TCP ports a session's processes listen on.
func (c *Conn) Ports(ctx context.Context, id string) ([]Port, error) {
	var out []Port
	err := c.Call(ctx, "ports", map[string]string{"id": id}, &out)
	return out, err
}

// RenameSession renames a session.
func (c *Conn) RenameSession(ctx context.Context, id, name string) error {
	return c.Call(ctx, "rename", map[string]string{"id": id, "name": name}, nil)
}

// Doctor asks the host to check itself.
func (c *Conn) Doctor(ctx context.Context) (HostInfo, error) {
	var d HostInfo
	err := c.Call(ctx, "doctor", nil, &d)
	return d, err
}

// Peek returns the newest output of a session, for a preview.
func (c *Conn) Peek(ctx context.Context, id string, n int) (data []byte, cols, rows int, err error) {
	var r struct {
		Data []byte `json:"data"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	err = c.Call(ctx, "peek", map[string]any{"id": id, "bytes": n}, &r)
	return r.Data, r.Cols, r.Rows, err
}

// SetupResult reports what host setup did.
type SetupResult = api.SetupResult

// Setup turns on linger and installs this terminal's terminfo on the host.
func (c *Conn) Setup(ctx context.Context) (SetupResult, error) {
	args := api.SetupArgs{Linger: true, Terminfo: map[string]string{}}
	if t := os.Getenv("TERM"); t != "" && t != "xterm-256color" && t != "xterm" {
		// TERM is passed as one argument to infocmp, never to a shell.
		if src, err := exec.CommandContext(ctx, "infocmp", "-x", t).Output(); err == nil { //nolint:gosec // G702: see above
			args.Terminfo[t] = string(src)
		}
	}
	var r SetupResult
	err := c.Call(ctx, "setup", args, &r)
	return r, err
}

// RoundTrip measures the round trip through the bridge.
func (c *Conn) RoundTrip(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	var out []Session
	if err := c.Call(ctx, "ls", nil, &out); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// SendText types text into a session without attaching.
func (c *Conn) SendText(ctx context.Context, id, text string, enter bool) error {
	return c.Call(ctx, "send", map[string]any{"id": id, "text": text, "enter": enter}, nil)
}

// RestoreResult is one session that restore looked at.
type RestoreResult = api.RestoreResult

// Restore asks the host to start again sessions a restart stopped.
func (c *Conn) Restore(ctx context.Context, all bool) ([]RestoreResult, error) {
	var out []RestoreResult
	err := c.Call(ctx, "restore", map[string]bool{"all": all}, &out)
	return out, err
}

// RemotePath turns a path under this machine's home folder into the same path
// under the host's home folder. Shells expand ~ before the client sees it, so
// "--dir ~/src/app" arrives as /Users/me/src/app and must become ~/src/app.
func RemotePath(p string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" || h == "/" {
		return p
	}
	if p == h {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, h+"/"); ok {
		return "~/" + rest
	}
	return p
}

// FoldersResult is what the new-session dialog completes folders from.
type FoldersResult = api.FoldersResult

// Folders completes a folder path on the host and suggests recent folders and repositories.
func (c *Conn) Folders(ctx context.Context, prefix string) (FoldersResult, error) {
	var r FoldersResult
	err := c.Call(ctx, "folders", map[string]any{"prefix": prefix, "limit": 12}, &r)
	return r, err
}
