package client

import (
	"context"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

// A raw manifest or installed-byte inventory cannot construct this original
// client selection. It is issued only from this client's checked release and
// actual accepted host/Source before any value command.
type newRuntimeSelection struct {
	conn     *Conn
	hostID   string
	source   hostStateSource
	manifest supply.Manifest
	items    []supply.Item
}

func cloneNewRuntimeManifest(m supply.Manifest) supply.Manifest {
	m.Items = slices.Clone(m.Items)
	m.Advisories = slices.Clone(m.Advisories)
	return m
}

func (c *Conn) prepareNewRuntime(ctx context.Context, settings Settings, level string) (*newRuntimeSelection, error) {
	if level != "gvisor" {
		return nil, grantOperationError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return nil, err
	}
	request, err := runtimeRequestFor(probe.Arch)
	if err != nil {
		return nil, err
	}
	items, err := gvisor.RuntimeItemsV1(request.Manifest, probe.Arch)
	if err != nil {
		return nil, err
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return nil, err
	}
	if hid != c.HostID() || !ValidHostID(hid) {
		return nil, grantIdentityError(c.Host)
	}
	if err := allowRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSilentUpgrade}, settings); err != nil {
		return nil, err
	}
	mode := settings.Images.Fetch
	if override, ok := ctx.Value(runtimeFetchKey{}).(string); ok && override != "" {
		mode = override
	}
	if _, err := EnsureRuntime(ctx, c, RuntimeOptions{Mode: mode, Probe: &probe}); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	return &newRuntimeSelection{conn: c, hostID: hid, source: c.hostTransport.source, manifest: cloneNewRuntimeManifest(request.Manifest), items: slices.Clone(items)}, nil
}

func (p *newRuntimeSelection) forArgs(ctx context.Context, c *Conn, current *supply.Manifest) (*supply.Manifest, error) {
	if p == nil || p.conn != c || p.hostID != c.HostID() || c.hostTransport == nil || !sameHostTransportSource(p.source, c.hostTransport.source) {
		return nil, grantOperationError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	if current != nil && !reflect.DeepEqual(*current, p.manifest) {
		return nil, grantOperationError()
	}
	manifest := cloneNewRuntimeManifest(p.manifest)
	return &manifest, nil
}
