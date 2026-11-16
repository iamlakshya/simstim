package client

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// newSecretPlan keeps original LOCAL selection, definitions and fetched values
// within one request. Public args, host rows and receipt metadata cannot mint it.
type newSecretPlan struct {
	aborting, attempted, retired     bool
	mu                               sync.Mutex
	closed, busy, fetched, published bool
	conn                             *Conn
	operation                        *settingsOperation
	selection                        *LocalAgentSelection
	header                           NewArgs
	layers                           []api.Layer
	creation                         *SandboxPolicy
	intent                           *api.SecretDeliveryIntent
	values                           map[string]api.Secret
	vm                               *VMNeedPlanV1
	runtime                          *newRuntimeSelection
}

// PrepareNewSecrets seals actual selected local agent/names/modes/bindings and
// commits the old-reader barrier BEFORE any value command. Keep the SAME
// GrantRequest through questions/retries and Close it when that operation ends.
// A microvm requires the actual same-connection pre-value Need builder result.
func (c *Conn) PrepareNewSecrets(ctx context.Context, a NewArgs, vm ...*VMNeedPlanV1) (_ NewArgs, err error) {
	r := grantRequestOf(ctx)

	if r == nil || len(vm) > 1 {
		return a, grantOperationError()
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return a, err
	}
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return a, err
	}
	if a.Wall == "" || a.Wall == "none" {
		return a, nil
	}
	r.mu.Lock()
	before := r.newSecrets
	r.mu.Unlock()
	if before != nil {
		return before.arguments(ctx, c, a)
	}
	if len(a.Secrets) != 0 {
		return a, grantOperationError()
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return a, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return a, err
	}
	if _, err := NormaliseSettingsSecrets(settings); err != nil {
		return a, err
	}
	selection, err := selectPreparedNewAgent(ctx, settings, c, a)
	if err != nil {
		return a, err
	}
	names := selection.Names()
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	defaults := map[string]api.SecretBind{}
	if agent, known := agents.Find(selection.Kind()); known {
		for _, binding := range agent.SecretBindings() {
			candidate := api.SecretBind{Hosts: slices.Clone(binding.Hosts), Header: binding.Header, Format: binding.Format}
			if prior, exists := defaults[binding.Env]; exists && !reflect.DeepEqual(prior, candidate) {
				return a, grantOperationError()
			}
			defaults[binding.Env] = candidate
		}
	}
	definitions := make(map[string]SecretDefinition, len(names))
	for _, name := range names {
		definitions[name] = settings.Secrets[name]
	}
	view, err := NormaliseSecretDefinitions(definitions, defaults)
	if err != nil {
		return a, err
	}
	wall := requestWallIntent(ctx)
	for _, name := range names {
		if wall.exposed[name] {
			intent.Modes[name] = api.SecretModeExposed
			continue
		}
		binding, bound := view.Binds[name]
		if !bound || a.Wall == "process" {
			return a, &UserError{Msg: "a walled secret needs a binding or its own explicit exposure", Hint: "Configure its HTTPS binding, or start a new request with --expose " + name + ".", Code: "secret_unbound", Kind: KindRefused}
		}
		intent.Modes[name] = api.SecretModeBound
		intent.Binds[name] = binding
	}
	modern := c.HasFeature(api.FeatureBoundSecrets)
	for _, mode := range intent.Modes {
		if mode == api.SecretModeBound && !modern {
			return a, c.grantHelperOld("preserve bound secret delivery")
		}
	}
	switch {
	case a.MemoryMax < 0:
		a.MemoryMax = 0
	case a.MemoryMax == 0:
		a.MemoryMax, err = SettingsMemoryMax(settings)
		if err != nil {
			return a, err
		}
	}
	a.HostLabel = c.Host.Name
	plan := &newSecretPlan{conn: c, operation: operation, selection: selection, layers: api.CloneLayers(a.Layers)}
	if modern {
		owned := api.CloneSecretDeliveryIntent(intent)
		plan.intent = &owned
	}
	if a.Wall == "gvisor" {
		plan.runtime, err = c.prepareNewRuntime(ctx, settings, a.Wall)
		if err != nil {
			return a, err
		}
	}
	if a.Wall == "microvm" {
		if len(vm) != 1 || vm[0] == nil {
			return a, vmPrepareUnavailable()
		}
		if _, err := vm[0].EncodedFor(c); err != nil {
			return a, err
		}
		r.mu.Lock()
		expectedVM := r.preparedVM
		r.mu.Unlock()
		if expectedVM != vm[0] {
			return a, vmPrepareUnavailable()
		}
		plan.vm = vm[0]
		limit, err := validateNewVMPlan(ctx, a, vm[0])
		if err != nil {
			return a, err
		}
		a.MemoryMax = limit
	} else if len(vm) != 0 && vm[0] != nil {
		return a, vmPrepareUnavailable()
	}
	plan.header = a
	owned, release, err := c.beginGrant(ctx, "prepare selected secrets", false)
	if err != nil {
		return a, err
	}
	defer release()
	if !ValidHostID(c.HostID()) {
		return a, grantIdentityError(c.Host)
	}
	tier, ceiling, err := c.newGrantTier(owned, names, a.SSHAgent)
	if err != nil {
		return a, err
	}
	_ = tier
	if plan.intent != nil {
		if err := commitSandboxBoundSchema(owned, c.Host); err != nil {
			return a, err
		}
	}
	for range 16 {
		id, token, idErr := r.identity()
		if idErr != nil {
			return a, idErr
		}
		sources, sourceErr := encodeRequestPolicySources(owned, a)
		if sourceErr != nil {
			return a, sourceErr
		}
		level, exposed := creationGrantFacts(owned)
		creation := &SandboxPolicy{PolicySources: sources, SessionID: id, HostID: c.HostID(), Ceiling: ceiling, SSHAgent: a.SSHAgent, Secrets: append([]string{}, names...), CreationWall: level, Exposed: exposed, CreatedAt: time.Now().UTC(), request: token, DeliveryIntent: plan.intent}
		if err = reserveSandbox(owned, c.Host, *creation); errors.Is(err, errSandboxIDTaken) {
			r.retryID()
			continue
		} else if err != nil {
			return a, err
		}
		// Retain the ACTUAL canonical reservation bytes under this SAME OP.
		// A prospective DTO has no canonical nested raw fields and cannot
		// later stand in for the exact ACK/abort compare-and-swap snapshot.
		saved, readErr := LoadSandboxPolicy(owned, c.Host, id)
		if readErr != nil {
			return a, readErr
		}
		if saved == nil || saved.request != token || saved.HostID != c.HostID() || saved.SessionID != id ||
			saved.FutureDelivery || saved.FutureLineage || saved.futureDeliveryReceipt || saved.futureInputOperation ||
			saved.deliveryReceipt != nil || saved.inputOperation != nil ||
			!reflect.DeepEqual(saved.DeliveryIntent, plan.intent) || !slices.Equal(saved.Secrets, creation.Secrets) ||
			!bytes.Equal(saved.PolicySources, creation.PolicySources) {
			return a, sandboxReceiptError()
		}
		plan.creation = cloneSandboxPolicy(saved)
		r.mu.Lock()
		if r.newSecrets != nil {
			r.mu.Unlock()
			return a, grantOperationError()
		}
		r.newSecrets = plan
		r.mu.Unlock()
		return plan.arguments(owned, c, a)
	}
	return a, grantOperationError()
}

func (p *newSecretPlan) arguments(ctx context.Context, c *Conn, a NewArgs) (NewArgs, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.published || p.conn != c || !p.selection.matchesAgentSelection(a.Agent, a.Argv) || !reflect.DeepEqual(p.layers, a.Layers) {
		return a, grantOperationError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return a, err
	}
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return a, err
	}
	if p.creation == nil || p.creation.CreationWall != a.Wall {
		return a, grantOperationError()
	}
	if a.Name != p.header.Name || a.Cwd != p.header.Cwd || a.Worktree != p.header.Worktree || a.Base != p.header.Base || a.Task != p.header.Task || a.Floor != p.header.Floor || a.DowngradedFrom != p.header.DowngradedFrom || a.Downgrade != p.header.Downgrade ||
		!slices.Equal(a.Mounts, p.header.Mounts) || !slices.Equal(a.Include, p.header.Include) {
		return a, grantOperationError()
	}
	if p.vm != nil {
		a.MemoryMax = p.header.MemoryMax
	} else if a.MemoryMax != p.header.MemoryMax {
		return a, grantOperationError()
	}
	a.HostLabel = p.header.HostLabel
	if p.runtime != nil {
		manifest, err := p.runtime.forArgs(ctx, c, a.Runtime)
		if err != nil {
			return a, err
		}
		a.Runtime = manifest
	} else if a.Runtime != nil {
		return a, grantOperationError()
	}
	a.ID = p.creation.SessionID
	if p.intent != nil {
		intent := api.CloneSecretDeliveryIntent(*p.intent)
		a.SecretDelivery = &intent
	} else {
		a.SecretDelivery = nil
	}
	if p.vm != nil {
		raw, err := p.vm.EncodedFor(c)
		if err != nil {
			return a, err
		}
		if len(a.VMNeedV1) != 0 && !bytes.Equal(a.VMNeedV1, raw) {
			return a, vmPrepareUnavailable()
		}
		a.VMNeedV1 = raw
	} else if len(a.VMNeedV1) != 0 {
		return a, vmPrepareUnavailable()
	}
	return a, nil
}

func (p *newSecretPlan) fetch(ctx context.Context, h Host, names []string, terminal bool) (_ map[string]api.Secret, err error) {
	p.mu.Lock()
	if p.closed || p.aborting || p.retired || p.busy || p.published || !SameEntry(h, p.selection.host) || !sameCanonicalSelectedNames(names, p.selection.names) {
		p.mu.Unlock()
		return nil, grantOperationError()
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	if input := newInputPreparationOf(ctx); input != nil {
		input.mu.Lock()
		err := input.discovery.revalidatePrimaryPolicy(ctx)
		if err == nil {
			err = input.revalidateInputFacts(ctx)
		}
		input.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	if p.fetched {
		values := maps.Clone(p.values)
		p.mu.Unlock()
		return values, nil
	}
	p.mu.Unlock()
	values, err := FetchSecrets(ctx, settings, names, terminal)
	if err != nil {
		return nil, err
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.values = maps.Clone(values)
	p.fetched = true
	p.mu.Unlock()
	return values, nil
}

// Close releases only this logical request's original local source/captures.
// Busy or incomplete cleanup retains ownership for the caller to join/retry.
func (r *GrantRequest) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.ending = true
	p, operation, inputs := r.newSecrets, r.preparationSettings, r.newInputs
	r.mu.Unlock()
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed {
			return nil
		}
		if p.busy {
			return agentModeResourceError()
		}
	}
	if err := r.closeLocalPurposeResources(); err != nil {
		return err
	}
	if err := inputs.close(); err != nil {
		return err
	}
	if operation != nil {
		if err := operation.Close(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.preparationClosed = true
	r.mu.Unlock()
	if p != nil {
		p.closed = true
		p.values = nil
	}
	return nil
}

func (p *newSecretPlan) final(ctx context.Context, c *Conn, a NewArgs, tier, ceiling Tier) (NewArgs, error) {
	result, err := p.arguments(ctx, c, a)
	if err != nil {
		return a, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.fetched || p.busy || !sameSelectedSecretValues(p.values, a.Secrets) || ceiling > p.creation.Ceiling || tier > ceiling {
		return a, grantOperationError()
	}
	if err := reserveSandbox(ctx, c.Host, *p.creation); err != nil {
		return a, err
	}
	return result, nil
}

func newPlanOf(ctx context.Context) *newSecretPlan {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.newSecrets
}

func (p *newSecretPlan) beginPublication() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.busy || p.published {
		return grantOperationError()
	}
	p.busy = true
	return nil
}
func (p *newSecretPlan) endPublication() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }

func sameCanonicalSelectedNames(a, b []string) bool {
	names, err := canonicalGrantNames(a)
	return err == nil && slices.Equal(names, b)
}
func sameSelectedSecretValues(a, b map[string]api.Secret) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		other, ok := b[name]
		if !ok || other != value {
			return false
		}
	}
	return true
}
