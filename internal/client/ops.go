package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
	return SessionArgs(a, cols, rows, LoadSettings())
}

// SessionArgs fills the terminal identity and settings-backed defaults of a
// new session from s.
func SessionArgs(a NewArgs, cols, rows int, s Settings) NewArgs {
	a.Term = os.Getenv("TERM")
	a.ColorTerm = os.Getenv("COLORTERM")
	a.TermProgram = os.Getenv("TERM_PROGRAM")
	a.TermProgramVersion = os.Getenv("TERM_PROGRAM_VERSION")
	a.Cols, a.Rows = cols, rows
	a.Inline = a.Inline || s.Agents.Inline
	return a
}

// CheckSessionName refuses name, a name that the user gives a session,
// when it looks like a session ID or is too long (api.CheckName), as the
// helper does too: an address with an ID's form always means an ID
// (cli-spec 9.2). The error has the code bad_name, and exit code 2.
func CheckSessionName(name string) error {
	if msg, hint, ok := api.CheckName(name); !ok {
		return &UserError{Msg: msg, Hint: hint, Code: "bad_name", Kind: KindUsage}
	}
	return nil
}

// Sessions lists a host's sessions, as the host sent them, without a row
// whose ID is not a session ID (ValidSessions).
func (c *Conn) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	err := c.Call(ctx, "ls", nil, &out)
	return ValidSessions(c.Host.Name, out), err
}

// NewSession starts a session. Unless a names a memory cap, the session gets
// the one in config.toml.
func (c *Conn) NewSession(ctx context.Context, a NewArgs) (*Session, error) {
	if err := CheckSessionName(a.Name); err != nil {
		return nil, err
	}
	a, err := withMemoryMax(a)
	if err != nil {
		return nil, err
	}
	a, err = withPackageSettings(a)
	if err != nil {
		return nil, err
	}
	a, err = c.withCacheSettings(a)
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
	if err := checkSessionID(c.Host.Name, s.ID); err != nil {
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
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old to make folders", Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)", Code: "helper_old"}
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

// DeleteNamed deletes the session id only while its name is still name,
// the name that the user confirmed: the host refuses the delete with
// session_renamed after a rename. The name is sent also when it is empty,
// as for a session with no name, which a rename may give one. A helper
// from before this ignores the name, and deletes.
func (c *Conn) DeleteNamed(ctx context.Context, id, name string) (DeleteResult, error) {
	var r DeleteResult
	err := c.Call(ctx, "delete", map[string]string{"id": id, "name": name}, &r)
	return r, err
}

// DeleteLines say what deleting the session s on host does, for the
// question before it: what stops, and what stays. The launcher's delete
// sheet and the rm command ask with the same lines. folder is the
// session's folder as the user reads it, such as ~/src/app. The host's
// words are cleaned of control and hidden characters.
//
// A folder stays, except a worktree that the session made: the host
// removes it with its branch when it has no changes and no new commits.
// The helper does not say which session made a worktree, so a folder in
// the helper's worktrees gets a line that is true either way.
func DeleteLines(s Session, host, folder string) []string {
	oneLine := strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ")
	agent := api.CleanText(oneLine.Replace(s.Agent), 200)
	host = api.CleanText(oneLine.Replace(host), 200)
	folder = api.CleanText(oneLine.Replace(folder), 1000)
	stays := fmt.Sprintf("The folder %s stays.", folder)
	if MaybeWorktree(s) {
		stays = fmt.Sprintf("If the session made the worktree %s, it goes with its branch, unless it has changes or new commits.", folder)
	}
	if Ended(s) {
		// Nothing runs that could lose work: say only what goes.
		return []string{fmt.Sprintf("This removes the session from %s.", host), stays}
	}
	return []string{
		fmt.Sprintf("This stops %s on %s and removes the session.", agent, host),
		stays + " Unsaved work in the agent is lost.",
	}
}

// MaybeWorktree reports whether the session s runs in the helper's folder
// of worktrees on the host. The session may have made that worktree, or
// may only have started in it.
func MaybeWorktree(s Session) bool {
	return strings.Contains(s.Cwd, "/"+brand.Name+"/worktrees/")
}

// Port is a TCP port a session listens on.
type Port = api.Port

// Ports lists the TCP ports a session's processes listen on.
func (c *Conn) Ports(ctx context.Context, id string) ([]Port, error) {
	var out []Port
	err := c.Call(ctx, "ports", map[string]string{"id": id}, &out)
	return out, err
}

// PortGrantResult is the supervisor's answer to GrantPort.
type PortGrantResult = api.PortGrantResult

// GrantPort asks a walled session's supervisor to expose guest port on a
// private host Unix socket for ssh -L forwarding (spec 7.4).
func (c *Conn) GrantPort(ctx context.Context, id string, port int) (PortGrantResult, error) {
	var out PortGrantResult
	err := c.Call(ctx, "port.grant", api.PortGrantArgs{ID: id, Port: port}, &out)
	return out, err
}

// RevokePort closes and removes a walled session's forwarded port Unix socket.
func (c *Conn) RevokePort(ctx context.Context, id string, port int) error {
	return c.Call(ctx, "port.revoke", api.PortGrantArgs{ID: id, Port: port}, nil)
}

// RenameSession renames a session.
func (c *Conn) RenameSession(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &UserError{Msg: "a name is needed", Hint: "Pass a non-empty name for the session.", Code: "bad_name", Kind: KindUsage}
	}
	if err := CheckSessionName(name); err != nil {
		return err
	}
	return c.Call(ctx, "rename", map[string]string{"id": id, "name": name}, nil)
}

// Doctor asks the host to check itself.
func (c *Conn) Doctor(ctx context.Context) (HostInfo, error) {
	var d HostInfo
	err := c.Call(ctx, "doctor", nil, &d)
	return d, err
}

// CapProbe runs the host's read-only sandbox probe (isolation spec 14.3).
// A helper without the "sandbox" feature fails with wall_helper_old.
func (c *Conn) CapProbe(ctx context.Context, releaseURL string) (api.CapProbe, error) {
	if !c.HasFeature("sandbox") {
		return api.CapProbe{}, &UserError{
			Msg:  "the helper on " + c.Host.Name + " is too old for sandboxes",
			Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)",
			Code: "wall_helper_old",
		}
	}
	var args any
	if releaseURL != "" {
		args = map[string]string{"release_url": releaseURL}
	}
	var out api.CapProbe
	err := c.Call(ctx, "sandbox.probe", args, &out)
	return out, err
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
