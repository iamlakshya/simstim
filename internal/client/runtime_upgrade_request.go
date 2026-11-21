package client

import (
	"context"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/runtimecache"
)

// NewRuntimeUpgradeGrantRequest owns only one host's runtime maintenance.
// It cannot select values, root steps, repositories, sessions or boot rights.
// Bind it before the actual connection/job; close it before another purpose.
func NewRuntimeUpgradeGrantRequest() *GrantRequest {
	return &GrantRequest{purpose: grantRuntimeUpgrade, operation: grantOpRuntimeUpgrade}
}

type runtimeUpgradeIntentV1 struct {
	mu       sync.Mutex
	request  *GrantRequest
	conn     *Conn
	stamp    *hostTransportStamp
	source   hostStateSource
	hostID   string
	runtime  runtimecache.RuntimeRequest
	mode     string
	settings *settingsOperation
	frames   map[uint64]runtimeUpgradeFrameV1
	parts    map[string]*runtimeUpgradePartV1
	fetches  map[string]uint64
}

func requireRuntimeUpgradeContext(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return ctx.Err()
		}
		return grantPurposeError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return grantPurposeError()
	}
	if requestGrantPurpose(r) == grantRemote {
		return requireGrantOperation(ctx, grantOpNew)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return err
	}
	if r.purpose != grantRuntimeUpgrade || r.operation != grantOpRuntimeUpgrade || r.Untrusted || r.SSHAgent || r.newIntent != nil || r.newSecrets != nil || r.freshChild != nil || r.newInputs != nil || r.root != nil || r.forge != nil || r.imageSave != nil {
		return grantPurposeError()
	}
	return nil
}

func cloneRuntimeUpgradeRequestV1(request runtimecache.RuntimeRequest) runtimecache.RuntimeRequest {
	request.Manifest = cloneNewRuntimeManifest(request.Manifest)
	if request.ImageDefaults != nil {
		items := slices.Clone(*request.ImageDefaults)
		request.ImageDefaults = &items
	}
	return request
}

// Freeze before status, confirmation or delivery. A terminal answer cannot
// approve another signed release, candidate, connection or delivery mode.
func sealRuntimeUpgradeIntentV1(ctx context.Context, c *Conn, runtime runtimecache.RuntimeRequest, mode string, settings *settingsOperation) (*runtimeUpgradeIntentV1, error) {
	if err := requireRuntimeUpgradeContext(ctx); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return nil, err
	}
	if before := r.runtimeUpgrade; before != nil {
		if before.request != r || before.conn != c || before.stamp != c.hostTransport || before.hostID != c.HostID() || !sameHostTransportSource(before.source, c.hostTransport.source) || before.mode != mode || before.settings != settings || !reflect.DeepEqual(before.runtime, runtime) {
			return nil, grantOperationError()
		}
		return before, nil
	}
	intent := &runtimeUpgradeIntentV1{request: r, conn: c, stamp: c.hostTransport, source: c.hostTransport.source, hostID: c.HostID(), runtime: cloneRuntimeUpgradeRequestV1(runtime), mode: mode, settings: settings, frames: map[uint64]runtimeUpgradeFrameV1{}, parts: map[string]*runtimeUpgradePartV1{}, fetches: map[string]uint64{}}
	r.runtimeUpgrade = intent
	return intent, nil
}

func (intent *runtimeUpgradeIntentV1) check(ctx context.Context) error {
	if intent == nil || intent.request == nil || intent.conn == nil || intent.stamp == nil || !ValidHostID(intent.hostID) || grantRequestOf(ctx) != intent.request || intent.conn.hostTransport != intent.stamp || intent.conn.HostID() != intent.hostID || !sameHostTransportSource(intent.source, intent.stamp.source) {
		return grantOperationError()
	}
	if err := requireRuntimeUpgradeContext(ctx); err != nil {
		return err
	}
	intent.request.mu.Lock()
	current := intent.request.runtimeUpgrade == intent
	intent.request.mu.Unlock()
	if !current {
		return grantOperationError()
	}
	return bindRequestTransportSource(ctx, intent.conn)
}

func requireRuntimeUpgradeOutboundV1(ctx context.Context, r *GrantRequest, outbound Outbound) error {
	if r == nil || grantRequestOf(ctx) != r || outbound.Kind != OutboundSilentUpgrade || outbound.Name != "" || requestGrantPurpose(r) != grantRuntimeUpgrade {
		return grantPurposeError()
	}
	if err := requireRuntimeUpgradeContext(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	intent := r.runtimeUpgrade
	r.mu.Unlock()
	return intent.check(ctx)
}
