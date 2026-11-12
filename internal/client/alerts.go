package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// AlertsInfo describes a host's alerts file, with its secrets hidden.
type AlertsInfo = api.AlertsInfo

// AlertsConfig is a host's alerts file: which events to send, and where.
type AlertsConfig = api.AlertsConfig

// AlertDestination is one place a host sends events.
type AlertDestination = api.AlertDestination

// AlertResult is what one destination did with a test event.
type AlertResult = api.AlertResult

// AlertLogEntry is one line of a host's alert delivery log.
type AlertLogEntry = api.AlertLogEntry

// HasFeature reports whether the host's helper offers a feature.
func (c *Conn) HasFeature(name string) bool {
	for _, f := range c.Features {
		if f == name {
			return true
		}
	}
	return false
}

// needAlerts fails, saying what to do, when the host's helper predates alerts.
func (c *Conn) needAlerts() error {
	if c.HasFeature("alerts") {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("the %s on %s is too old for alerts", brand.HostBinary, c.Host.Name),
		Hint: brand.Name + " host add " + c.Host.Name + " (installs this version's helper)",
		Code: "helper_old",
	}
}

// callAlerts makes one alerts request, after checking the helper has them.
func (c *Conn) callAlerts(ctx context.Context, op string, args, out any) error {
	if err := c.needAlerts(); err != nil {
		return err
	}
	err := c.Call(ctx, op, args, out)
	var ue *UserError
	if errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "unknown request") {
		return c.needAlerts()
	}
	return err
}

// Alerts describes the host's alerts file.
func (c *Conn) Alerts(ctx context.Context) (AlertsInfo, error) {
	var r AlertsInfo
	err := c.callAlerts(ctx, "alerts.get", nil, &r)
	return r, err
}

// SetAlerts writes the host's alerts file: the whole file, or with add, new
// destinations and the settings cfg sets, over what the file has.
func (c *Conn) SetAlerts(ctx context.Context, cfg AlertsConfig, add bool) (AlertsInfo, error) {
	var r AlertsInfo
	err := c.callAlerts(ctx, "alerts.set", api.AlertsSetArgs{Config: cfg, Add: add}, &r)
	return r, err
}

// TestAlerts sends a test event to every destination now, and reports what
// each did. event is "test" or another kind, such as "needs_input".
func (c *Conn) TestAlerts(ctx context.Context, event string) ([]AlertResult, error) {
	var r []AlertResult
	err := c.callAlerts(ctx, "alerts.test", api.AlertsTestArgs{Event: event}, &r)
	return r, err
}

// AlertsLog returns the newest n lines of the host's delivery log.
func (c *Conn) AlertsLog(ctx context.Context, n int) ([]AlertLogEntry, error) {
	var r []AlertLogEntry
	err := c.callAlerts(ctx, "alerts.log", api.AlertsLogArgs{Lines: n}, &r)
	return r, err
}

// AlertsOff removes the host's alerts file. removed is false if it had none.
func (c *Conn) AlertsOff(ctx context.Context) (removed bool, err error) {
	var r struct {
		Removed bool `json:"removed"`
	}
	err = c.callAlerts(ctx, "alerts.off", nil, &r)
	return r.Removed, err
}

// SecretFromCommand runs a command line on this machine, such as a keychain
// read, and returns what it prints, trimmed. The value is never printed: an
// error says only that the command failed.
func SecretFromCommand(ctx context.Context, line string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", line)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", line)
	}
	cmd.Stdin = os.Stdin // a keychain may ask for a password
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	hint := "Run the command by hand, and check that it prints only the token."
	if err != nil {
		return "", &UserError{Msg: "the token command failed (" + err.Error() + ")", Hint: hint, Code: "token_command"}
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" || strings.ContainsAny(tok, "\r\n") {
		return "", &UserError{Msg: "the token command printed nothing, or more than one line", Hint: hint, Code: "token_command"}
	}
	return tok, nil
}
