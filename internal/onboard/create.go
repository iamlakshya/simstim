package onboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

// named lists the providers whose machines are named provider.MachineName
// of the host: a stop can find what they had begun to make, and delete it.
// An E2B sandbox gets its ID only from E2B's answer.
var named = map[string]bool{Lima: true, Docker: true}

// PowersOffWhenIdle reports whether a provider's machines stop themselves
// with the helper's idle timer (VMs), rather than with a lease kept by the
// client (sandboxes), or not at all (containers).
func PowersOffWhenIdle(p string) bool { return p == Lima }

// CheckCreate checks a new host's name and idle time before anything is
// made.
func CheckCreate(p provider.Provider, req provider.Request) error {
	if !client.ValidHostName(req.Name) {
		return &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", req.Name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
	}
	if req.IdleStop != "" && req.IdleStop != "never" {
		if d, err := time.ParseDuration(req.IdleStop); err != nil || d < time.Minute {
			return &client.UserError{Msg: fmt.Sprintf("%q is not an idle time", req.IdleStop), Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.", Code: "bad_value", Kind: client.KindUsage}
		}
	}
	if _, err := client.FindHost(req.Name); err == nil {
		return &client.UserError{Msg: "there is already a host called " + req.Name, Hint: brand.Name + " host create " + p.Name() + " <another-name>", Code: "host_exists"}
	}
	return nil
}

// Create makes a machine for a new host with p, and saves the host at once,
// so that `host destroy` can always find what was made. It does not install
// the helper: the caller connects the host next.
//
// If ctx ends while the provider works, as when the user presses Ctrl+C,
// Create deletes the machine it had begun to make, when the provider names
// machines after their hosts and no such machine was there before. The
// error then says what was cleaned up.
func Create(ctx context.Context, p provider.Provider, req provider.Request) (client.Host, error) {
	if err := CheckCreate(p, req); err != nil {
		return client.Host{}, err
	}
	probe := client.Host{Name: req.Name, Machine: &client.Machine{Provider: p.Name(), ID: provider.MachineName(req.Name)}}
	fresh := false
	if named[p.Name()] {
		if lc, err := p.Open(probe); err == nil {
			st, err := lc.Status(ctx)
			fresh = err == nil && st == client.MachineMissing
		}
	}
	h, err := p.Create(ctx, req)
	if err == nil {
		err = client.SaveHost(h)
	}
	if err == nil {
		return h, nil
	}
	if ctx.Err() == nil {
		return client.Host{}, err
	}
	stopped := &client.UserError{Msg: fmt.Sprintf("stopped while making %s; nothing was saved", req.Name), Hint: brand.Name + " setup", Code: "cancelled", Kind: client.KindCancelled}
	switch {
	case fresh:
		// A fresh context: the user's has ended, and the clean-up must still run.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if lc, oerr := p.Open(probe); oerr == nil {
			if derr := lc.Destroy(cctx); derr != nil {
				stopped.Msg = fmt.Sprintf("stopped while making %s, and the %s machine %s may be left: %v", req.Name, p.Name(), probe.Machine.ID, derr)
				stopped.Hint = "Delete it with " + p.Name() + "'s own tools."
				return client.Host{}, stopped
			}
		}
		stopped.Msg = fmt.Sprintf("stopped while making %s; the half-made %s machine %s was deleted", req.Name, p.Name(), probe.Machine.ID)
	case !named[p.Name()]:
		stopped.Msg = fmt.Sprintf("stopped while making %s; if %s had begun a machine, it stops by itself when its lease ends", req.Name, p.Name())
		stopped.Hint = "Check " + p.Name() + "'s own console for a machine labelled " + req.Name + "."
	}
	return client.Host{}, stopped
}

// SetIdle installs, or with "never" removes, the helper's idle timer on a
// VM that powers itself off. It returns what the helper says it did.
func SetIdle(ctx context.Context, h client.Host) (string, error) {
	if h.Machine == nil {
		return "", errors.New(h.Name + " was not made by a provider")
	}
	after := h.Machine.IdleStop
	if after == "" {
		after = lima.DefaultIdleStop
	}
	ictx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	t, err := client.HostTransport(ictx, h, true)
	if err != nil {
		return "", err
	}
	out, err := t.Run(ictx, client.RemoteHelper(client.HelperVersion())+" idle setup "+after, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
