package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

// RuntimeUpgradeOptions carries delivery preferences only. Shared-host
// confirmation remains scoped to the actual caller's original RemoteR.
type RuntimeUpgradeOptions struct{ RuntimeOptions }

// UpgradeRuntimeOnUse is the pre-value New or explicit maintenance path.
// It retains the original request, native settings reader and connection.
// Its result is delivery metadata; the actual caller must still consume its
// original retained request and private post-delivery physical driver probe.
func UpgradeRuntimeOnUse(ctx context.Context, c *Conn, options RuntimeUpgradeOptions) (api.RuntimeStatus, error) {
	var empty api.RuntimeStatus
	if ctx == nil || c == nil || grantRequestOf(ctx) == nil {
		return empty, grantOperationError()
	}
	if err := requireRuntimeUpgradeContext(ctx); err != nil {
		return empty, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return empty, err
	}
	requestOwner := grantRequestOf(ctx)
	operation, err := requestOwner.borrowNewSettings(ctx)
	if err != nil {
		return empty, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return empty, err
	}
	stamp, hid := c.hostTransport, c.HostID()
	var intent *runtimeUpgradeIntentV1
	check := func() error {
		if grantRequestOf(ctx) != requestOwner || c.hostTransport != stamp || c.HostID() != hid || !ValidHostID(hid) {
			return grantOperationError()
		}
		if intent != nil {
			if err := intent.check(ctx); err != nil {
				return err
			}
		}
		return errors.Join(operation.Revalidate(ctx), bindRequestTransportSource(ctx, c))
	}
	if err := check(); err != nil {
		return empty, err
	}
	probe := options.Probe
	if probe == nil {
		value, err := c.CapProbe(ctx, "")
		if err != nil {
			return empty, err
		}
		probe = &value
	}
	if probe.OS != "linux" {
		return empty, &UserError{Msg: "runtime delivery needs a Linux host", Hint: "Use a Linux host on amd64 or arm64.", Code: "runtime_platform"}
	}
	// Only the client's embedded signed-release manifest supplies expectation.
	// The host probe chooses no URL, item, digest, command or credential.
	request, err := runtimeRequestFor(probe.Arch)
	if err != nil {
		return empty, err
	}
	if len(request.Manifest.Items) == 0 {
		return empty, &UserError{Msg: "this client has no runtime files for the host architecture", Hint: "Install a signed release with the selected sandbox runtime.", Code: "runtime_missing", Kind: KindRefused}
	}
	if findings := request.Manifest.OwnFindings(); len(findings) != 0 {
		return empty, &UserError{Msg: "this client's selected runtime matches a known security advisory", Hint: "Install a current release before starting this sandbox.", Code: "runtime_advisory", Kind: KindRefused}
	}
	mode := options.Mode
	if mode == "" {
		mode = settings.Images.Fetch
		if override, ok := ctx.Value(runtimeFetchKey{}).(string); ok && override != "" {
			mode = override
		}
	}
	mode, err = RuntimeFetchMode(mode)
	if err != nil {
		return empty, err
	}
	intent, err = sealRuntimeUpgradeIntentV1(ctx, c, request, mode, operation)
	if err != nil {
		return empty, err
	}
	status, err := runtimeStatus(ctx, c, request)
	if err != nil {
		return empty, err
	}
	if err := check(); err != nil {
		return empty, err
	}
	complete := len(status.Transfers) == 0
	for _, item := range status.Items {
		complete = complete && item.Present
	}
	if complete {
		// Idle runtime.status retains and checks original role descriptors on
		// this bridge. It does not promote inventory to driver readiness.
		return status, check()
	}
	tier, currentID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return empty, err
	}
	if currentID != hid {
		return empty, grantIdentityError(c.Host)
	}
	if err := allowRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSilentUpgrade}, settings); err != nil {
		return empty, err
	}
	if err := check(); err != nil {
		return empty, err
	}
	options.Probe = probe
	options.Mode = mode
	runtimeLine(options.Progress, "Updating the runtime on %s.\n", CleanLine(c.Host.Name))
	delivery := context.WithValue(ctx, runtimeUpgradePublicationKeyV1{}, intent)
	if _, err := EnsureRuntime(delivery, c, options.RuntimeOptions); err != nil {
		return empty, err
	}
	if err := check(); err != nil {
		return empty, err
	}
	result, err := runtimeReady(ctx, c, request)
	if err != nil {
		return empty, err
	}
	if err := check(); err != nil {
		return empty, err
	}
	return result, nil
}

// UpgradeRuntimeOnUse keeps the concrete connection behind launcher facades.
func (c *Conn) UpgradeRuntimeOnUse(ctx context.Context, options RuntimeUpgradeOptions) (api.RuntimeStatus, error) {
	return UpgradeRuntimeOnUse(ctx, c, options)
}
