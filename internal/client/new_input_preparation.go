package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

// NewInputOptions is the local frontend intent for one new session. It never
// enters an RPC. Session contains the original local flags, before merging.
type NewInputOptions struct {
	OriginalCWD string
	Seeds       []string
	SeedDirty   bool
	HostClone   bool
	Profile     string
	Session     api.LayerValues
	Environment []EnvironmentOption
}

type newInputPreparation struct {
	mu                  sync.Mutex
	conn                *Conn
	operation           *settingsOperation
	options             NewInputOptions
	cwd                 string
	hostRepository      string
	choice              NewArgs
	discovery           *inputSeedDiscovery
	metadata            LocalGitMetadataOpener
	layers              []api.Layer
	repository          *api.RepositoryPolicy
	selection           *LocalAgentSelection
	pin                 *api.PolicyClientInputsPin
	plan                *SessionInputPlan
	ready, busy, closed bool
	payload             bool
	fetched             bool
	values              map[string]api.Secret
}

func cloneNewInputOptions(options NewInputOptions) NewInputOptions {
	options.Seeds = slices.Clone(options.Seeds)
	options.Session = api.CloneLayerValues(options.Session)
	options.Environment = slices.Clone(options.Environment)
	return options
}

// PrepareNewInputPolicy resolves the original local layers and primary policy
// from the SAME retained Settings operation used by runtime and secret fetches.
// A dirty primary owns one prospective commit, reused by final bundle capture.
func (c *Conn) PrepareNewInputPolicy(ctx context.Context, a NewArgs, options NewInputOptions) (_ NewArgs, err error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return a, grantOperationError()
	}
	if options.HostClone != (a.Cwd != "") {
		return a, copyInputError("input_primary", "the original host-directory choice and primary input selection disagree")
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return a, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return a, err
	}
	if !options.HostClone && len(options.Seeds) != 0 && !c.HasFeature(inputtransfer.PrimaryPolicyFeature) {
		return a, c.grantHelperOld("verify the exact laptop primary policy before starting its member")
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return a, err
	}
	r.mu.Lock()
	p := r.newInputs
	if p == nil {
		if a.Inputs != nil || a.Environment != nil || len(a.Secrets) != 0 {
			r.mu.Unlock()
			return a, grantOperationError()
		}
		p = &newInputPreparation{conn: c, operation: operation, options: cloneNewInputOptions(options), choice: a}
		p.choice.Argv = slices.Clone(a.Argv)
		r.newInputs = p
	}
	r.mu.Unlock()
	if len(a.Secrets) != 0 {
		secrets := newPlanOf(ctx)
		valid := false
		if secrets != nil {
			secrets.mu.Lock()
			valid = !secrets.closed && !secrets.aborting && !secrets.retired && !secrets.busy && secrets.conn == c &&
				secrets.operation == operation && secrets.fetched && sameSelectedSecretValues(secrets.values, a.Secrets)
			secrets.mu.Unlock()
		} else {
			p.mu.Lock()
			valid = !p.closed && !p.busy && !p.payload && p.fetched && p.conn == c && p.operation == operation && sameSelectedSecretValues(p.values, a.Secrets)
			p.mu.Unlock()
		}
		if !valid {
			return a, grantOperationError()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.busy || p.conn != c || p.operation != operation || !reflect.DeepEqual(p.options, options) ||
		!sameNewInputChoice(p.choice, a) {
		return a, grantOperationError()
	}
	if p.ready {
		a, err = p.policyArguments(ctx, a)
		if err != nil || p.plan == nil {
			return a, err
		}
		return p.inputArguments(ctx, a)
	}
	if a.Inputs != nil || a.Environment != nil || len(a.Secrets) != 0 {
		return a, grantOperationError()
	}
	p.busy = true
	defer func() { p.busy = false }()
	settings, err := operation.Settings(ctx)
	if err != nil {
		return a, err
	}
	p.metadata = func(ctx context.Context) (LocalGitMetadataSource, error) {
		return newOrdinaryLocalGitMetadataSource(ctx, operation)
	}
	// Check the complete configured namespace before Getwd or any native
	// environment lookup. This borrower never closes the original source.
	metadata, err := p.metadata(ctx)
	if err != nil {
		return a, err
	}
	keyErr := metadata.CheckKeys(ctx, gitMetadataCheckKeys(localWindows()))
	if keyErr == nil {
		keyErr = metadata.Revalidate(ctx)
	}
	if err := errors.Join(keyErr, metadata.Close()); err != nil {
		return a, err
	}
	if p.cwd != "" {
		options.OriginalCWD = p.cwd
	}
	if options.OriginalCWD == "" {
		options.OriginalCWD, err = os.Getwd()
		if err != nil {
			return a, err
		}
	}
	if !filepath.IsAbs(options.OriginalCWD) || options.SeedDirty && len(options.Seeds) == 0 {
		return a, copyInputError("input_cwd", "the original local input directory or seed selection is invalid")
	}
	// Keep the caller's exact options for retry comparison; the resolved CWD
	// belongs to this private planner and never to outgoing arguments.
	p.cwd = options.OriginalCWD
	if len(options.Seeds) != 0 && p.discovery == nil {
		p.discovery, err = discoverSessionSeeds(ctx, options.OriginalCWD, options.Seeds, options.HostClone, p.metadata)
		if err != nil {
			return a, err
		}
	}
	origin := ""
	var file *api.RepositoryPolicy
	if primary := p.discovery.primary(); primary != nil {
		origin = primary.origin
		file, err = p.discovery.primaryPolicy(ctx, options.SeedDirty, a.Base, p.metadata)
	} else if options.HostClone {
		var repo RepoInfo
		repo, err = SessionRepoPolicy(ctx, c.Host, a.Cwd)
		if err == nil {
			origin, file = repo.Origin, repo.Policy
			p.hostRepository = path.Base(repo.Root)
			if !api.InputRepoName(p.hostRepository) {
				return a, copyInputError("seed_plan", "the host primary repository has no protected basename")
			}
		} else {
			var user *UserError
			if errors.As(err, &user) && user.Code == "not_a_repo" {
				err = nil
			}
		}
	}
	if err != nil {
		return a, err
	}
	p.layers, err = ResolvePolicyLayers(settings, c.Host, origin, options.Profile, options.Session)
	if err != nil {
		return a, err
	}
	a.Layers = api.CloneLayers(p.layers)
	a, err = c.prepareRepositoryPolicyFile(ctx, a, file)
	if err != nil {
		return a, err
	}
	p.repository = file
	p.selection, err = selectNewAgentSecrets(settings, c.Host, a, a.Layers)
	if err != nil {
		return a, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return a, err
	}
	if err := p.discovery.revalidatePrimaryPolicy(ctx); err != nil {
		return a, err
	}
	p.ready = true
	return a, nil
}

func sameNewInputChoice(original, actual NewArgs) bool {
	return original.Agent == actual.Agent && slices.Equal(original.Argv, actual.Argv) && original.Cwd == actual.Cwd &&
		original.Name == actual.Name && original.Base == actual.Base && original.Worktree == actual.Worktree
}

func newInputPreparationOf(ctx context.Context) *newInputPreparation {
	if r := grantRequestOf(ctx); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.newInputs
	}
	return nil
}

func (p *newInputPreparation) policyArguments(ctx context.Context, a NewArgs) (NewArgs, error) {
	if p == nil || p.closed || !p.ready || !sameNewInputChoice(p.choice, a) {
		return a, grantOperationError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return a, err
	}
	if err := p.discovery.revalidatePrimaryPolicy(ctx); err != nil {
		return a, err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return a, err
	}
	a.Layers = api.CloneLayers(p.layers)
	return p.conn.prepareRepositoryPolicyFile(ctx, a, p.repository)
}

// PrepareNewInputs captures the input declaration and ordinary environment
// after the actual policy and wall are resolved, before any value command.
// Repeating it reuses the original capture, nonce, source pin and commit.
func (c *Conn) PrepareNewInputs(ctx context.Context, a NewArgs) (NewArgs, error) {
	p := newInputPreparationOf(ctx)
	if p == nil {
		return a, grantOperationError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.busy || !p.ready || p.conn != c || !sameNewInputChoice(p.choice, a) || !reflect.DeepEqual(p.layers, a.Layers) {
		return a, grantOperationError()
	}
	if err := checkWallGrantArgs(ctx, a.Wall, a.SSHAgent); err != nil {
		return a, err
	}
	var err error
	a, err = p.policyArguments(ctx, a)
	if err != nil {
		return a, err
	}
	if p.plan != nil {
		if err := p.allowInputFacts(ctx, false); err != nil {
			return a, err
		}
		return p.inputArguments(ctx, a)
	}
	if a.Inputs != nil || a.Environment != nil {
		return a, grantOperationError()
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return a, err
	}
	local, _, err := egress.MergeLayers(a.Layers)
	if err != nil {
		return a, policyError(err)
	}
	checked, _, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return a, policyError(err)
	}
	values := api.CloneLayerValues(api.LayerValues{Sandbox: local.Sandbox}).Sandbox
	copyMax, seedMax, err := api.InputLimits(values)
	if err != nil {
		return a, err
	}
	checkedCopy, checkedSeed, err := api.InputLimits(checked.Sandbox)
	if err != nil {
		return a, err
	}
	copyMax, seedMax = min(copyMax, checkedCopy), min(seedMax, checkedSeed)
	copyLimit, seedLimit := strconv.FormatInt(copyMax, 10), strconv.FormatInt(seedMax, 10)
	values.CopyMax, values.SeedMax = &copyLimit, &seedLimit
	environment, err := ResolveSessionEnvironment(ctx, settings, checked.Sandbox.Env, p.options.Environment)
	if err != nil {
		return a, err
	}
	p.pin, err = api.NewPolicyClientInputsPin(values.CopyIn, p.cwd, copyMax, seedMax)
	if err != nil {
		return a, policySourceStateError()
	}
	raw, err := encodeLocalPolicySourcesWithInputs(a, p.pin)
	if err != nil {
		return a, err
	}
	walled := a.Wall != "" && a.Wall != "none"
	if !walled && (len(values.CopyIn) != 0 || len(p.options.Seeds) != 0 || len(a.Include) != 0) {
		return a, copyInputError("not_walled", "laptop files and repositories need a sandbox")
	}
	if (walled || len(environment) != 0) && (!c.HasFeature(inputtransfer.Feature) || !c.HasFeature(inputtransfer.FinishFeature)) {
		return a, c.grantHelperOld("prepare and finish the original laptop input operation")
	}
	p.payload = walled
	if !walled {
		p.pin = nil
	}
	p.busy = true
	defer func() { p.busy = false }()
	p.plan, err = buildSessionInputPlan(ctx, inputPlanSelection{values: values, sourcePin: raw, originalCwd: p.cwd,
		mode: inputPlanNew, seeds: p.options.Seeds, seedDirty: p.options.SeedDirty, hostClone: p.options.HostClone,
		hostRepository: p.hostRepository, discovery: p.discovery, primaryIncludes: slices.Clone(a.Include),
		base: a.Base, metadata: p.metadata, resolvedEnv: environment})
	if err != nil {
		return a, err
	}
	if err := p.allowInputFacts(ctx, false); err != nil {
		return a, err
	}
	return p.inputArguments(ctx, a)
}

func (p *newInputPreparation) inputArguments(ctx context.Context, a NewArgs) (NewArgs, error) {
	if p == nil || p.closed || p.plan == nil || !p.ready {
		return a, grantOperationError()
	}
	if !p.payload && (p.fetched && !sameSelectedSecretValues(p.values, a.Secrets) || !p.fetched && len(a.Secrets) != 0) {
		return a, grantOperationError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return a, err
	}
	if err := p.discovery.revalidatePrimaryPolicy(ctx); err != nil {
		return a, err
	}
	summary, err := p.plan.Summary()
	if err != nil {
		return a, err
	}
	environment := &api.SessionEnvironment{Version: api.SessionEnvironmentV1, Values: p.plan.Environment()}
	if a.Inputs != nil && !a.Inputs.Equal(summary) || a.Environment != nil && !reflect.DeepEqual(a.Environment, environment) {
		return a, grantOperationError()
	}
	if a.Wall != "" && a.Wall != "none" {
		a.Inputs = &summary
	}
	if len(environment.Values) != 0 || a.Wall != "" && a.Wall != "none" {
		a.Environment = environment
	}
	public := a
	public.Layers = api.ProjectClientInputSources(a.Layers)
	raw, err := json.Marshal(public)
	if err != nil {
		return a, err
	}
	frame, err := json.Marshal(proto.Request{ID: ^uint64(0), Op: "new", Args: raw})
	if err != nil || len(frame)+1 > proto.MaxFrame {
		return a, copyInputError("inputs_too_big", "the local input and environment declaration exceeds the request bound")
	}
	return a, nil
}

func encodeRequestPolicySources(ctx context.Context, a NewArgs) (json.RawMessage, error) {
	if p := newInputPreparationOf(ctx); p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed || !p.ready || p.payload && p.pin == nil || p.plan == nil || !sameNewInputChoice(p.choice, a) || !reflect.DeepEqual(p.layers, a.Layers) {
			return nil, policySourceStateError()
		}
		if _, err := p.inputArguments(ctx, a); err != nil {
			return nil, err
		}
		return encodeLocalPolicySourcesWithInputs(a, p.pin)
	}
	if a.Inputs != nil || a.Environment != nil {
		return nil, grantOperationError()
	}
	return encodeLocalPolicySources(a)
}

func (p *newInputPreparation) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.busy {
		return agentModeResourceError()
	}
	var err error
	if p.plan != nil {
		err = p.plan.Close()
	} else {
		err = p.discovery.Close()
	}
	if err == nil {
		p.closed = true
		p.values = nil
	}
	return err
}

func selectPreparedNewAgent(ctx context.Context, settings Settings, c *Conn, a NewArgs) (*LocalAgentSelection, error) {
	p := newInputPreparationOf(ctx)
	if p == nil {
		return selectNewAgentSecrets(settings, c.Host, a, a.Layers)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.ready || p.conn != c || p.plan == nil || p.selection == nil ||
		!p.selection.matchesAgentSelection(a.Agent, a.Argv) || !reflect.DeepEqual(p.layers, a.Layers) {
		return nil, grantOperationError()
	}
	if _, err := p.inputArguments(ctx, a); err != nil {
		return nil, err
	}
	return p.selection, nil
}
