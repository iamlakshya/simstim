package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

type resumeNamedSSHKeyPublicationKey struct{}

// This same logical Resume owns its local sources and the server's opaque
// continuation. Only a genuine cold preparation can precede a key command.
type resumeNamedSSHKeyPlan struct {
	mu              sync.Mutex
	work            sync.Mutex
	lifetime        context.Context
	cancel          context.CancelFunc
	closing         bool
	nativeAttempted bool
	memoryWarned    bool
	request         *GrantRequest
	conn            *Conn
	stamp           *hostTransportStamp
	operation       *settingsOperation
	creation        *SandboxPolicy
	id              string
	ceiling         Tier
	names           []string
	layers          []api.Layer
	prepared        *api.ResumePrepareResultV1
	rewind          *RewindPreparation
	metadata        []byte
	values          map[string]api.Secret
	fetched         bool
	closed          bool
}

func (*resumeNamedSSHKeyPlan) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func prepareResumeNamedSSHKeys(ctx context.Context, c *Conn, id string) (*resumeNamedSSHKeyPlan, error) {
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	r.mu.Lock()
	before := r.namedResume
	r.mu.Unlock()
	if before != nil {
		return before, before.check(ctx, c, false)
	}
	creation, err := LoadSandboxPolicy(ctx, c.Host, id)
	if err != nil {
		return nil, err
	}
	if creation == nil || creation.CreationWall == "none" {
		return nil, nil
	}
	if creation.FutureLineage || creation.HostID != c.HostID() {
		return nil, namedSSHKeySourceError()
	}
	if !c.HasFeature(api.FeatureResumePreparationV1) || !c.HasFeature(api.FeatureNamedSSHKeysV1) {
		return nil, c.grantHelperOld("prepare an original native resume")
	}
	operation, creation, sourceTier, err := retainedNamedSSHPolicySourceV1(ctx, c, id)
	if err != nil {
		return nil, err
	}
	layers, err := resumeImageLayersV1(ctx, operation, c.Host, creation)
	if err != nil {
		return nil, err
	}
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return nil, policyError(err)
	}
	names, err := applyNamedSSHKeyChoicesV1(creation, policy.Sandbox.SSHKeys)
	if err != nil {
		return nil, err
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return nil, err
	}
	if hid != c.HostID() {
		return nil, grantIdentityError(c.Host)
	}
	if r.requestContext == nil {
		return nil, grantOperationError()
	}
	lifetime, cancel := context.WithCancel(r.requestContext)
	retained := false
	defer func() {
		if !retained {
			cancel()
		}
	}()
	n := &resumeNamedSSHKeyPlan{lifetime: lifetime, cancel: cancel, request: r, conn: c, stamp: c.hostTransport, operation: operation, creation: cloneSandboxPolicy(creation), id: id, ceiling: min(tier, sourceTier, creation.Ceiling, requestCeiling(ctx, tier)), names: slices.Clone(names), layers: api.CloneLayers(layers), values: map[string]api.Secret{}}
	if err := n.check(ctx, c, false); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return nil, err
	}
	if r.namedResume != nil {
		return nil, namedSSHKeySourceError()
	}
	r.namedResume = n
	retained = true
	return n, nil
}

func (n *resumeNamedSSHKeyPlan) check(ctx context.Context, c *Conn, grants bool) error {
	if n == nil {
		return nil
	}
	if ctx == nil || c == nil || n.conn != c || n.stamp != c.hostTransport || n.request != grantRequestOf(ctx) || n.operation == nil {
		return namedSSHKeySourceError()
	}
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	n.mu.Lock()
	closed := n.closed || n.closing
	n.mu.Unlock()
	if closed {
		return namedSSHKeySourceError()
	}
	operation, current, sourceTier, err := retainedNamedSSHPolicySourceV1(ctx, c, n.id)
	if err != nil {
		return err
	}
	if operation != n.operation || !sameSandboxCreation(current, n.creation) {
		return namedSSHKeySourceError()
	}
	layers, err := resumeImageLayersV1(ctx, n.operation, c.Host, n.creation)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(layers, n.layers) {
		return namedSSHKeySourceError()
	}
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return policyError(err)
	}
	names, err := applyNamedSSHKeyChoicesV1(current, policy.Sandbox.SSHKeys)
	if err != nil {
		return err
	}
	if !slices.Equal(names, n.names) {
		return namedSSHKeySourceError()
	}
	settings, err := n.operation.Settings(ctx)
	if err != nil {
		return err
	}
	if grants {
		view, err := NormaliseSettingsSecrets(settings)
		if err != nil {
			return err
		}
		tier, hid, err := grantHostTier(ctx, c.Host)
		if err != nil {
			return err
		}
		if hid != c.HostID() {
			return grantIdentityError(c.Host)
		}
		n.mu.Lock()
		ceiling := n.ceiling
		n.mu.Unlock()
		tier = min(tier, sourceTier, ceiling, current.Ceiling, requestCeiling(ctx, tier))
		n.mu.Lock()
		n.ceiling = min(n.ceiling, tier)
		n.mu.Unlock()
		for _, name := range n.names {
			if view.Kinds[name] != "ssh-key" || view.Commands[name] == "" {
				return namedSSHKeySourceError()
			}
			if err := allowRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSSHKey, Name: name}, settings); err != nil {
				return err
			}
		}
	}
	return n.operation.Revalidate(ctx)
}

func (n *resumeNamedSSHKeyPlan) arguments(ctx context.Context, c *Conn, a ResumeArgs) (context.Context, ResumeArgs, error) {
	if n == nil {
		if a.Prepared != nil || a.NamedSSHKeys != nil {
			return ctx, a, namedSSHKeySourceError()
		}
		return ctx, a, nil
	}
	if a.ID != n.id {
		return ctx, a, namedSSHKeySourceError()
	}
	owned, finish, err := n.begin(ctx)
	if err != nil {
		return ctx, a, err
	}
	defer finish()
	ctx = owned
	if err := n.check(ctx, c, false); err != nil {
		return ctx, a, err
	}
	prepared, err := n.prepareNative(ctx, c, a)
	if err != nil {
		return ctx, a, err
	}
	if a.Prepared != nil && !reflect.DeepEqual(a.Prepared, prepared.Prepared) {
		return ctx, a, namedSSHKeySourceError()
	}
	a.Prepared = api.ClonePreparedResumeV1(prepared.Prepared)
	if err := n.checkRemote(ctx); err != nil {
		return ctx, a, err
	}
	if prepared.Mode == "warm" {
		if a.NamedSSHKeys != nil {
			return ctx, a, namedSSHKeySourceError()
		}
	} else {
		if err := n.fetch(ctx, c); err != nil {
			return ctx, a, err
		}
		n.mu.Lock()
		keys := &api.NamedSSHKeysV1{Version: 1, Names: slices.Clone(n.names), Values: maps.Clone(n.values)}
		fetched := n.fetched
		n.mu.Unlock()
		if len(n.names) == 0 {
			keys = nil
		} else if !fetched || !keys.Valid() {
			return ctx, a, namedSSHKeySourceError()
		}
		if a.NamedSSHKeys != nil && !reflect.DeepEqual(a.NamedSSHKeys, keys) {
			return ctx, a, namedSSHKeySourceError()
		}
		a.NamedSSHKeys = keys
	}
	return context.WithValue(ctx, resumeNamedSSHKeyPublicationKey{}, n), a, nil
}

func (n *resumeNamedSSHKeyPlan) checkRemote(ctx context.Context) error {
	if err := n.check(ctx, n.conn, false); err != nil {
		return err
	}
	n.mu.Lock()
	prepared := n.prepared
	rewind := n.rewind
	n.mu.Unlock()
	if rewind != nil {
		return rewind.checkRemote(ctx)
	}
	if prepared == nil || !prepared.Valid() {
		return namedSSHKeySourceError()
	}
	var result api.ResumePrepareResultV1
	if err := n.conn.Call(resumePreparationRPCContextV1(ctx), "resume.prepared.check-v1", prepared.Prepared, &result); err != nil {
		return err
	}
	if !result.Valid() || !reflect.DeepEqual(result, *prepared) {
		return namedSSHKeySourceError()
	}
	return n.check(ctx, n.conn, false)
}

func (n *resumeNamedSSHKeyPlan) fetch(ctx context.Context, c *Conn) error {
	if err := n.check(ctx, c, true); err != nil {
		return err
	}
	settings, err := n.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	for _, name := range n.names {
		if err := n.checkRemote(ctx); err != nil {
			return err
		}
		if n.rewind != nil {
			if err := n.rewind.checkDeliveryGrantsV1(ctx); err != nil {
				return err
			}
		}
		if err := n.check(ctx, c, true); err != nil {
			return err
		}
		n.mu.Lock()
		_, done := n.values[name]
		n.mu.Unlock()
		if done {
			continue
		}
		value, err := runSecret(ctx, name, view.Commands[name], InputAllowed(ctx))
		if err != nil {
			return err
		}
		if err := n.checkRemote(ctx); err != nil {
			return err
		}
		if err := n.check(ctx, c, true); err != nil {
			return err
		}
		n.mu.Lock()
		n.values[name] = value
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.fetched = true
	n.mu.Unlock()
	return n.checkRemote(ctx)
}

// The physical writer already owns its ordered turn. This check is local;
// a nested RPC here would deadlock that same writer. The server independently
// checks the retained native owner before it consumes the final request.
func (n *resumeNamedSSHKeyPlan) beforeRequest(ctx context.Context, c *Conn, request proto.Request, arguments any) error {
	if request.Op == api.OpRewindCommitV1 {
		return n.beforeRewindRequestV1(ctx, c, request, arguments)
	}
	a, ok := arguments.(ResumeArgs)
	if !ok || request.Op != "resume" || a.ID != n.id {
		return namedSSHKeySourceError()
	}
	if err := n.check(ctx, c, false); err != nil {
		return err
	}
	encoded, err := json.Marshal(a)
	if err != nil || !bytes.Equal(encoded, request.Args) {
		return namedSSHKeySourceError()
	}
	metadata, err := n.metadataBytes(a)
	if err != nil {
		return err
	}
	n.mu.Lock()
	prepared, before := n.prepared, bytes.Clone(n.metadata)
	values, fetched := maps.Clone(n.values), n.fetched
	n.mu.Unlock()
	defer clear(values)
	if prepared == nil || !prepared.Valid() || !reflect.DeepEqual(a.Prepared, prepared.Prepared) || !bytes.Equal(metadata, before) {
		return namedSSHKeySourceError()
	}
	if prepared.Mode == "warm" {
		if a.NamedSSHKeys != nil {
			return namedSSHKeySourceError()
		}
		return nil
	}
	if err := n.check(ctx, c, true); err != nil {
		return err
	}
	if len(n.names) == 0 {
		if a.NamedSSHKeys != nil {
			return namedSSHKeySourceError()
		}
		return nil
	}
	expected := &api.NamedSSHKeysV1{Version: 1, Names: slices.Clone(n.names), Values: values}
	if !fetched || !expected.Valid() || !reflect.DeepEqual(a.NamedSSHKeys, expected) {
		return namedSSHKeySourceError()
	}
	tier, hid, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	n.mu.Lock()
	ceiling := n.ceiling
	n.mu.Unlock()
	tier = min(tier, ceiling, n.creation.Ceiling, requestCeiling(ctx, tier))
	for _, name := range n.names {
		if err := recordRequestGrant(ctx, c.Host, hid, tier, Outbound{Kind: OutboundSSHKey, Name: name}); err != nil {
			return err
		}
	}
	return n.check(ctx, c, true)
}

func (n *resumeNamedSSHKeyPlan) close() error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closing = true
	if n.cancel != nil {
		n.cancel()
	}
	n.mu.Unlock()
	n.work.Lock()
	defer n.work.Unlock()
	n.mu.Lock()
	prepared, attempted := n.prepared, n.nativeAttempted
	rewind := n.rewind
	n.mu.Unlock()
	if rewind != nil {
		rewind.mu.Lock()
		closed := rewind.closed
		rewind.mu.Unlock()
		if !closed {
			return rewindSourceErrorV1()
		}
	}
	if prepared == nil && attempted {
		n.conn.Close()
		return namedSSHKeySourceError()
	}
	if prepared != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var result struct{}
		if err := n.conn.Call(ctx, "resume.prepared.cancel-v1", prepared.Prepared, &result); err != nil {
			n.conn.Close()
			return err // EOF or a local process exit never proves remote native cleanup.
		}
	}
	n.mu.Lock()
	clear(n.values)
	n.values = nil
	n.fetched = false
	n.closed = true
	n.mu.Unlock()
	return nil
}

// begin joins the actual local fetch/preparation before Close can release its
// source. Cancellation interrupts owned commands; it never proves native exit.
func (n *resumeNamedSSHKeyPlan) begin(ctx context.Context) (context.Context, func(), error) {
	n.work.Lock()
	n.mu.Lock()
	closed := n.closed || n.closing
	n.mu.Unlock()
	if closed {
		n.work.Unlock()
		return ctx, nil, namedSSHKeySourceError()
	}
	owned, cancel := context.WithCancel(ctx)
	joined := make(chan struct{})
	stop := context.AfterFunc(n.lifetime, func() { defer close(joined); cancel() })
	finish := func() {
		if !stop() {
			<-joined
		}
		cancel()
		n.work.Unlock()
	}
	if err := owned.Err(); err != nil {
		finish()
		return ctx, nil, err
	}
	return owned, finish, nil
}
