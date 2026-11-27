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
	if req.IdleStop != "" {
		if err := client.CheckIdleTime(req.IdleStop); err != nil {
			return err
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
// Provider results and stored provenance alone never authorise cleanup. A
// failed binding/save retains and reports the exact returned resource. Only an
// actual closed native producer capability could authorise original-locator
// cleanup; the current historical birth token supplies no such capability.
func Create(ctx context.Context, p provider.Provider, req provider.Request) (client.Host, error) {
	ctx, release, err := client.BeginHostCreationContext(ctx, CreationRequest(p, req))
	if err != nil {
		return client.Host{}, err
	}
	defer release()
	if err := CheckCreate(p, req); err != nil {
		return client.Host{}, err
	}
	if err := client.CheckHostCreationRemovalContext(ctx, req.Name); err != nil {
		return client.Host{}, err
	}
	// Begin owns the one provider key stage. Do not shadow its captured
	// stage or refresh its absent-name source after an external reply.
	if err := client.StartHostCreationContext(ctx); err != nil {
		return client.Host{}, err
	}
	defer client.EndHostCreationAttemptContext(ctx)
	h, err := p.Create(ctx, req)
	if err == nil {
		if err := validateCreationResult(p, req, h); err != nil {
			// A malformed identity cannot authorise Destroy of the returned
			// name or ID. Preserve and report its actual partial outcome.
			return h, err
		}
		bound, bindErr := client.BindHostCreationResultContext(ctx, h)
		if bindErr != nil {
			return h, unsaved(ctx, p, req, h, bindErr)
		}
		h = bound
		// A new host: the save compares with no entry (cli-spec 11.3),
		// publishes any pin that the provider staged, and records the
		// provider's tier (isolation spec 14.8.3).
		if err := client.UpdateHosts(ctx, client.HostChange{Kind: "host create", Now: &h, Pin: true, Tier: client.ProviderTier(p.Name())}); err != nil {
			return h, unsaved(ctx, p, req, h, err)
		}
		return h, nil
	}
	if h.Machine != nil || h.Name != "" {
		// Presence supplies reporting data only, never Bind/Update/Destroy
		// authority. Preserve the actual value/private token on uncertainty.
		return h, partialCreationResult(ctx, h, err)
	}
	if ctx.Err() == nil {
		return client.Host{}, err
	}
	stopped := &client.UserError{Msg: fmt.Sprintf("stopped while making %s. Nothing was saved and an unverified partial %s resource may remain", req.Name, p.Name()),
		Hint: "Inspect " + p.Name() + "'s own console and this creation attempt before retrying or deleting any resource.", Code: "cancelled", Kind: client.KindCancelled}
	if p.Name() == E2B {
		stopped.Msg += ". A sandbox may stop when its lease ends"
	}

	return client.Host{}, stopped
}

// unsaved retains the exact resource after a failed binding/save. Neither the
// raw returned Host nor historical Source data is a native cleanup capability.
func unsaved(ctx context.Context, p provider.Provider, req provider.Request, h client.Host, saveErr error) error {
	client.EndHostCreationAttemptContext(ctx)
	return partialCreationResult(ctx, h, saveErr)
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
