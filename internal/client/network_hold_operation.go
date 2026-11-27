package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The actual accepted connection and locally retained source construct this
// operation. A descriptor, masked Host or resolution DTO never grants it.
type networkHoldOperation struct {
	mu                sync.Mutex
	request           *GrantRequest
	conn              *Conn
	stamp             *hostTransportStamp
	args              api.NetworkHoldActionArgs
	settings          *settingsOperation
	config            *networkHoldConfigV1
	creation          *SandboxPolicy
	tier              Tier
	maximum           api.NetworkHoldMaximum
	descriptor        *api.NetworkHoldDescriptor
	answerResult      *api.NetworkHoldAnswerResult
	resolution        *api.NetworkHoldResolution
	selected          FileLayer
	busy, attempted   bool
	closed, published bool
	phase             string
}
type networkHoldPublicationKey struct{}
type networkHoldFilePublicationKey struct{}

func (*networkHoldOperation) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func networkHoldSourceError() error {
	return &UserError{Msg: "the held-request action lost its original local intent or source", Hint: "Select the current request again using a new logical action on its original host.", Code: "network_hold_source", Kind: KindRefused}
}
func networkHoldPositiveProtocolErrorV1() error {
	return &UserError{Msg: "the host returned an invalid held-request response", Hint: "Update the host helper and select the current request again.", Code: "network_holds_protocol", Kind: KindRefused}
}

func newNetworkHoldOperation(ctx context.Context, c *Conn, args api.NetworkHoldActionArgs) (*networkHoldOperation, error) {
	if err := requireNetworkHoldRequestContext(ctx); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil || args.Version != api.NetworkHoldV1 || !api.ValidSessionID(args.Session) || !api.ValidNetworkHoldID(args.Request) || args.Revision != 0 || args.Maximum != "" || !validHoldActionScope(args.Scope) {
		return nil, networkHoldSourceError()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return nil, err
	}
	if c.agentOnly || c.hostTransport.kind != hostTransportSaved || !ValidHostID(c.HostID()) || c.HostIDChanged() {
		return nil, networkHoldSourceError()
	}
	if !c.HasFeature(api.FeatureNetworkHolds) || !c.HasFeature(api.FeatureNetworkPublicOnly) {
		return nil, c.grantHelperOld("preserve held-request source and address restrictions")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	r.mu.Lock()
	before := r.networkHold
	r.mu.Unlock()
	if before == nil {
		return nil, networkHoldSourceError()
	}
	before.mu.Lock()
	if before.closed || before.busy || before.request != r || before.args != args {
		before.mu.Unlock()
		return nil, networkHoldSourceError()
	}
	if before.conn != nil {
		matches := before.conn == c && before.stamp == c.hostTransport
		before.mu.Unlock()
		if !matches {
			return nil, networkHoldSourceError()
		}
		return before, before.checkLocal(ctx)
	}
	before.busy, before.conn, before.stamp = true, c, c.hostTransport
	before.mu.Unlock()
	defer before.finish()
	settings, err := r.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, args.Session, c.HostID())
	if err != nil {
		return nil, err
	}
	if !tier.Valid() {
		return nil, networkHoldSourceError()
	}
	if creation == nil {
		tier = min(tier, TierThrowaway)
	}
	o := before
	o.mu.Lock()
	o.settings, o.creation, o.tier, o.maximum = settings, cloneSandboxPolicy(creation), tier, api.NetworkHoldExistingPolicy
	if tier < TierOwn {
		o.maximum = api.NetworkHoldPublicOnly
	}
	o.mu.Unlock()
	if args.Scope == api.NetworkHoldRepo || args.Scope == api.NetworkHoldProfile {
		config, err := openNetworkHoldConfigV1(ctx, o)
		if err != nil {
			return nil, err
		}
		o.mu.Lock()
		o.config = config
		o.mu.Unlock()
	}
	if err := o.checkLocal(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.networkHold != o {
		r.mu.Unlock()
		return nil, networkHoldSourceError()
	}
	r.mu.Unlock()
	return o, nil
}

func validHoldActionScope(scope api.NetworkHoldScope) bool {
	return scope == api.NetworkHoldOnce || scope == api.NetworkHoldSession || scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile
}

func (o *networkHoldOperation) checkLocal(ctx context.Context) error {
	if o == nil {
		return networkHoldSourceError()
	}
	o.mu.Lock()
	closed := o.closed
	r, c, settings, stamp := o.request, o.conn, o.settings, o.stamp
	creation, args := cloneSandboxPolicy(o.creation), o.args
	config := o.config
	o.mu.Unlock()
	if closed || r == nil || settings == nil || stamp == nil || r != grantRequestOf(ctx) || c == nil || stamp != c.hostTransport {
		return networkHoldSourceError()
	}
	r.mu.Lock()
	same := r.networkHold == o
	r.mu.Unlock()
	if !same {
		return networkHoldSourceError()
	}
	if err := requireNetworkHoldRequestContext(ctx); err != nil {
		return err
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return err
	}
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	if err := settings.Revalidate(ctx); err != nil {
		return err
	}
	if args.Scope == api.NetworkHoldRepo || args.Scope == api.NetworkHoldProfile {
		if config == nil {
			return networkHoldSourceError()
		}
		if err := config.check(ctx); err != nil {
			return err
		}
	}
	tier, current, err := EffectiveSessionTier(ctx, c.Host, args.Session, c.HostID())
	if err != nil {
		return err
	}
	if !tier.Valid() || !sameSandboxCreation(current, creation) {
		return networkHoldSourceError()
	}
	if current != nil {
		if current.FutureLineage {
			return futureLineageError()
		}
		if current.FutureDelivery {
			return futureDeliveryError()
		}
		if current.futureDeliveryReceipt || current.futureInputOperation {
			return futureSandboxReceiptError()
		}
	}
	o.mu.Lock()
	if o.closed || o.request != r || o.conn != c || o.stamp != stamp || o.settings != settings || o.config != config {
		o.mu.Unlock()
		return networkHoldSourceError()
	}
	o.tier = min(o.tier, tier, requestCeiling(ctx, tier))
	if current != nil {
		o.tier = min(o.tier, current.Ceiling)
	}
	if o.tier < TierOwn {
		o.maximum = api.NetworkHoldPublicOnly
	}
	descriptor := o.descriptor
	o.mu.Unlock()
	if descriptor != nil && !time.Now().Before(descriptor.Expires) {
		return networkHoldSourceError()
	}
	return nil
}

func (o *networkHoldOperation) start(ctx context.Context) error {
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.busy {
		return networkHoldSourceError()
	}
	o.busy = true
	return nil
}

func (o *networkHoldOperation) close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if o.busy {
		o.mu.Unlock()
		return agentModeResourceError()
	}
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.busy = true
	config := o.config
	o.mu.Unlock()
	if config != nil {
		if err := config.close(); err != nil {
			o.finish()
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.busy = false
	o.closed = true
	o.conn, o.stamp, o.request, o.settings = nil, nil, nil, nil
	o.config = nil
	o.creation, o.descriptor, o.resolution, o.answerResult = nil, nil, nil, nil
	return nil
}
func (o *networkHoldOperation) finish() { o.mu.Lock(); o.busy = false; o.phase = ""; o.mu.Unlock() }

func (o *networkHoldOperation) prepare(ctx context.Context) error {
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	descriptor := o.descriptor
	o.mu.Unlock()
	if descriptor != nil {
		return nil
	}
	args := api.NetworkHoldDescribeArgs{Version: api.NetworkHoldV1, Session: o.args.Session, Request: o.args.Request}
	var raw json.RawMessage
	if err := o.call(ctx, api.OpPolicyDescribe, args, &raw); err != nil {
		return err
	}
	decoded, err := decodeHoldDescriptor(raw, o.args.Request)
	if err != nil {
		return err
	}
	// Known private/special numeric targets cannot create an exception, even
	// on Own. DNS public class is descriptive and promises no socket address.
	if decoded.Class == api.NetworkHoldPrivateTarget {
		return networkHoldSourceError()
	}
	if decoded.Reason == api.NetworkHoldUploadBudget && o.args.Scope != api.NetworkHoldOnce {
		return networkHoldUploadScopeErrorV1()
	}
	o.mu.Lock()
	o.descriptor = &decoded
	o.mu.Unlock()
	return o.checkLocal(ctx)
}

func networkHoldUploadScopeErrorV1() error {
	return &UserError{Msg: "an upload budget hold supports only once or refuse",
		Hint: "Change network.upload_budget in the selected policy layer with policy edit, then apply that policy.", Code: "upload_budget_scope", Kind: KindRefused}
}

func (o *networkHoldOperation) positiveArgs() api.NetworkHoldActionArgs {
	o.mu.Lock()
	defer o.mu.Unlock()
	args := o.args
	if o.descriptor != nil {
		args.Revision = o.descriptor.Revision
	}
	args.Maximum = o.maximum
	return args
}
