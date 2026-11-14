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
		return &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", req.Name), Hint: "Use letters, digits, dots, dashes and underscores, with no dot at the end.", Code: "bad_name", Kind: client.KindUsage}
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
// so that `host rm --destroy` can always find what was made. It does not install
// the helper: the caller connects the host next.
//
// If ctx ends before the provider returns a machine, Create deletes the
// machine it had begun to make only when the provider names machines after
// their hosts and no such machine was there before. If the provider returns
// a machine but its initial save fails, bounded cleanup deletes that exact
// machine before the host lock is released. The error says what was cleaned
// up. A host whose initial save committed stays saved if later preparation
// stops; cancellation permits no further host write.
func Create(ctx context.Context, p provider.Provider, req provider.Request) (client.Host, error) {
	// No host rm of this name runs while the machine is made and saved. A
	// caller that saves the host again after it, as `host create` and the
	// guided setup do, holds the lock in ctx through that save too.
	ctx, unlock, err := client.LockHost(ctx, req.Name, "host create")
	if err != nil {
		return client.Host{}, err
	}
	defer unlock()
	if err := CheckCreate(p, req); err != nil {
		return client.Host{}, err
	}
	// A new host starts with no record of an earlier removal. The record
	// goes before anything is made, so a failure here leaves no machine
	// that no saved host names.
	if err := client.ClearRemoval(req.Name); err != nil {
		var ue *client.UserError
		if !errors.As(err, &ue) {
			return client.Host{}, err
		}
		return client.Host{}, &client.UserError{Msg: ue.Msg + ", so nothing was made", Hint: ue.Hint, Code: ue.Code}
	}
	probe := client.Host{Name: req.Name, Machine: &client.Machine{Provider: p.Name(), ID: provider.MachineName(req.Name)}}
	fresh := false
	if named[p.Name()] {
		if lc, err := p.Open(probe); err == nil {
			st, err := lc.Status(ctx)
			fresh = err == nil && st == client.MachineMissing
		}
	}
	// A provider that pins the new machine's key, as Lima's does, pins it
	// into a stage, which the save moves to the host's own pin.
	ctx, abort, err := client.StagePinOwned(ctx, req.Name)
	if err != nil {
		return client.Host{}, err
	}
	defer abort()
	h, err := p.Create(ctx, req)
	if err == nil && h.Machine != nil {
		// The stamp tells this machine from an earlier one of the same
		// name.
		h.Machine.Created = time.Now().UTC().Format(time.RFC3339Nano)
		// A new host: the save compares with no entry (cli-spec 11.3),
		// publishes any pin that the provider staged, and records the
		// provider's tier (isolation spec 14.8.3).
		if err := client.UpdateHosts(ctx, client.HostChange{Kind: "host create", Now: &h, Pin: true, Tier: client.ProviderTier(p.Name())}); err != nil {
			return client.Host{}, unsaved(ctx, p, req, h, err)
		}
		return h, nil
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
		lc, cleanupErr := p.Open(probe)
		if cleanupErr == nil {
			cleanupErr = lc.Destroy(cctx)
		}
		if cleanupErr != nil {
			stopped.Msg = fmt.Sprintf("stopped while making %s, and the %s machine %s may be left: %v", req.Name, p.Name(), probe.Machine.ID, cleanupErr)
			stopped.Hint = "Delete it with " + p.Name() + "'s own tools."
			return client.Host{}, stopped
		}
		stopped.Msg = fmt.Sprintf("stopped while making %s; the half-made %s machine %s was deleted", req.Name, p.Name(), probe.Machine.ID)
	case !named[p.Name()]:
		stopped.Msg = fmt.Sprintf("stopped while making %s; if %s had begun a machine, it stops by itself when its lease ends", req.Name, p.Name())
		stopped.Hint = "Check " + p.Name() + "'s own console for a machine labelled " + req.Name + "."
	}
	return client.Host{}, stopped
}

// unsaved deletes machine h, which Create made but could not save, as a
// cancel does: no machine stays that no saved host names. Its error says
// why the save failed, and what became of the machine. When the delete
// fails too, it names the machine that stays.
func unsaved(ctx context.Context, p provider.Provider, req provider.Request, h client.Host, saveErr error) error {
	out := &client.UserError{Msg: saveErr.Error(), Code: "save_failed",
		Hint: fmt.Sprintf("%s host create %s %s (after you fix the cause)", brand.Name, p.Name(), req.Name)}
	var ue *client.UserError
	switch {
	case errors.Is(saveErr, context.Canceled):
		out = &client.UserError{Msg: fmt.Sprintf("stopped while making %s; nothing was saved", req.Name), Hint: brand.Name + " setup", Code: "cancelled", Kind: client.KindCancelled}
	case errors.As(saveErr, &ue):
		kept := *ue
		out = &kept
	}
	// A fresh context: the user's may have ended, and the clean-up must
	// still run.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	what := fmt.Sprintf("the %s machine %s", p.Name(), h.Machine.ID)
	lc, err := p.Open(h)
	if err == nil {
		err = lc.Destroy(cctx)
	}
	if err != nil {
		out.Msg = fmt.Sprintf("%s, and %s stays: %v", out.Msg, what, err)
		out.Hint = "Delete it with " + p.Name() + "'s own tools, then run the command again."
		return out
	}
	out.Msg += ", so " + what + " was deleted again"
	return out
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
