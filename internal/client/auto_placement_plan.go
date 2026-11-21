package client

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// AutoPlacementPlanV1 retains local selection and advisory facts. It owns no
// remote grant, native admission or credential values. HostAttemptPlanV1 owns
// each actual New request and connection separately.
type AutoPlacementPlanV1 struct {
	mu         sync.Mutex
	ctx        context.Context
	selection  *HostSelectionV1
	operation  *settingsOperation // borrowed from the still-open selection
	store      *placementStore
	args       NewArgs
	need       PlacementNeed
	options    NewInputOptions
	wake       AutoPlacementOptionsV1
	origin     string
	settings   PlacementSettings
	hosts      []Host
	candidates []*autoPlacementCandidate
	rows       []AutoPlacementRowV1
	summary    AutoPlacementSummaryV1
	dials      []*ownedNewDialV1 // read-only facts contacts, including failed hello
	closed     bool
	prepared   bool
}

// AutoPlacementRowV1 contains only bounded local display metadata.
type AutoPlacementRowV1 struct {
	Verdict        string
	Image          string
	RoundTrip      time.Duration
	RoundTripKnown bool
	RoomKnown      bool
	Host           string
	Eligible       bool
	Reason         string
	Wake           string
	Level          string
	Room           int64
}

// PrepareAutoPlacementV1 completes the whole local pool and secret-minimum
// pass before any provider status or Dial. A non-nil result must be closed,
// including on error: failed facts dials may still own native processes.
func PrepareAutoPlacementV1(ctx context.Context, selection *HostSelectionV1, args NewArgs, need PlacementNeed, wakeOptions ...AutoPlacementOptionsV1) (*AutoPlacementPlanV1, error) {
	if ctx == nil || selection == nil || grantRequestOf(ctx) != nil || len(wakeOptions) > 1 {
		return nil, grantOperationError()
	}
	if err := selection.Check(ctx); err != nil {
		return nil, err
	}
	if !selection.Automatic() {
		return nil, grantOperationError()
	}
	if err := unpreparedAutoPlacementArgsV1(args); err != nil {
		return nil, err
	}
	selection.mu.Lock()
	operation, options := selection.operation, cloneNewInputOptions(selection.options)
	selection.mu.Unlock()
	// Explicit auto still needs the actual laptop policy origin even though
	// H19 correctly did not need it to resolve an explicit host selector.
	origin, err := readHostRuleOrigin(ctx, operation, options)
	if err != nil {
		return nil, err
	}
	options.OriginalCWD = origin.originalCwd
	p := &AutoPlacementPlanV1{ctx: ctx, selection: selection, operation: operation, options: options,
		origin: origin.origin, args: cloneAutoPlacementArgsV1(args), need: clonePlacementNeedV1(need)}
	if len(wakeOptions) == 1 {
		p.wake = wakeOptions[0]
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return p, err
	}
	if err := ValidateSettings(settings); err != nil {
		return p, err
	}
	p.settings = settings.Placement
	p.settings.Prefer = slices.Clone(p.settings.Prefer)
	if err := validatePlacementSettings(p.settings); err != nil {
		return p, err
	}
	file, err := loadHostsFile()
	if err != nil {
		return p, err
	}
	tiers, err := LoadTiersContext(ctx, file.Hosts)
	if err != nil {
		return p, err
	}
	seen := make(map[string]bool, len(file.Hosts))
	for _, host := range file.Hosts {
		if !ValidHostName(host.Name) || seen[host.Name] {
			return p, placementConfigError()
		}
		seen[host.Name] = true
		p.hosts = append(p.hosts, cloneObservationHost(host))
		candidate, reason, err := p.localCandidateV1(ctx, settings, host, tiers.Of(host.Name))
		if err != nil {
			return p, err
		}
		if reason != "" {
			p.rows = append(p.rows, AutoPlacementRowV1{Host: host.Name, Verdict: "skipped", Image: "-", Reason: reason})
			continue
		}
		p.candidates = append(p.candidates, candidate)
	}
	// This boundary is deliberately after every host's complete local pass.
	if err := selection.Check(ctx); err != nil {
		return p, err
	}
	p.store, err = openPlacementStore(ctx, operation)
	if err == nil {
		err = p.gatherV1(ctx)
	}
	return p, err
}

func (p *AutoPlacementPlanV1) localCandidateV1(ctx context.Context, settings Settings, host Host, tier Tier) (*autoPlacementCandidate, string, error) {
	layers, err := ResolvePolicyLayers(settings, host, p.origin, p.options.Profile, p.options.Session)
	if err != nil {
		return nil, "", err
	}
	a := cloneAutoPlacementArgsV1(p.args)
	a.Layers = layers
	merged, _, err := egress.MergeLayers(layers)
	if err != nil {
		return nil, "", policyError(err)
	}
	need := clonePlacementNeedV1(p.need)
	// Image metadata is derived from the actual effective image and selected
	// wall below. Caller byte counts cannot omit a missing microvm image.
	need.ImageDigest, need.ImageBytes, need.ImageBytesKnown = "", 0, false
	choice, minimum, err := chooseAutoPlacementWallV1(host, tier, a, layers, need)
	if err != nil {
		var user *UserError
		if errors.As(err, &user) && user.Kind == KindRefused {
			return nil, CleanLine(user.Msg), nil
		}
		return nil, "", err
	}
	level := choice.Level
	if level != "microvm" && level != "gvisor" {
		return nil, "automatic placement requires gvisor or microvm", nil
	}
	if wallLevelRank(level) < wallLevelRank(minimum) {
		return nil, "the actual pinned/configured wall is below this automatic request's minimum", nil
	}
	need.MinLevel = minimum
	size, err := ResolveNewVMOptions(a, "auto")
	if err != nil {
		return nil, "", err
	}
	need.Memory, need.Disk = size.GuestRAM, size.DiskMax
	if a.MemoryMax > 0 {
		if level == "microvm" {
			budget, err := vmneed.BudgetFor(size.GuestRAM)
			if err != nil || a.MemoryMax != budget.Limit {
				return nil, "the original memory cap differs from the complete microvm size", nil
			}
		}
	}
	selected, err := selectNewAgentSecrets(settings, host, a, layers)
	if err != nil {
		return nil, "", err
	}
	names := selected.Names()
	if merged.Classifier.Key != nil && *merged.Classifier.Key != "" {
		names = append(names, *merged.Classifier.Key)
	}
	// SSH keys and the classifier key are selected credentials too. They may
	// never evade the pre-provider minimum pass just because they are not env.
	pendingGrants := false
	for _, item := range []struct {
		kind  OutboundKind
		names []string
	}{
		{OutboundSecret, names}, {OutboundSSHKey, merged.Sandbox.SSHKeys},
	} {
		for _, name := range item.names {
			minimum, err := minimumSecretTier(settings, name)
			if err != nil {
				return nil, "", err
			}
			need.RequiresOwn = need.RequiresOwn || minimum == TierOwn
			err = Allow(host, min(tier, autoPlacementCeilingV1(need)), Outbound{Kind: item.kind, Name: name}, settings)
			if err != nil {
				var question *ConfirmError
				if errors.As(err, &question) {
					pendingGrants = true
					continue
				}
				var user *UserError
				if errors.As(err, &user) && user.Kind == KindRefused {
					return nil, "a selected credential needs another host or tier", nil
				}
				return nil, "", err
			}
		}
	}
	if a.SSHAgent {
		return nil, "automatic placement cannot forward the full SSH agent", nil
	}
	if err := validatePlacementNeed(need); err != nil {
		return nil, "", err
	}
	reason, err := poolPlacementReason(host, tier, need)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	source, err := captureHostSelectionSource(ctx, host)
	if err != nil {
		return nil, "", err
	}
	runtimeUpgrade := Allow(host, tier, Outbound{Kind: OutboundSilentUpgrade}, settings) == nil
	return &autoPlacementCandidate{host: cloneObservationHost(host), tier: tier, need: need,
		localArgs: a, selectedWall: level, originalSource: source, runtimeUpgrade: runtimeUpgrade, pendingGrants: pendingGrants}, "", nil
}

func autoPlacementCeilingV1(need PlacementNeed) Tier {
	if need.Untrusted {
		return TierThrowaway
	}
	return TierOwn
}

// Names returns an owned ranked list; names alone never bind an attempt.
func (p *AutoPlacementPlanV1) Names() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.prepared || p.ctx.Err() != nil {
		return nil
	}
	names := make([]string, len(p.candidates))
	for i, candidate := range p.candidates {
		names[i] = candidate.host.Name
	}
	return names
}

func (p *AutoPlacementPlanV1) Rows() []AutoPlacementRowV1 {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	return slices.Clone(p.rows)
}

func (p *AutoPlacementPlanV1) candidateV1(ctx context.Context, name string) (*autoPlacementCandidate, error) {
	if p == nil || ctx == nil {
		return nil, grantOperationError()
	}
	if err := p.selection.Check(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.prepared || p.ctx.Err() != nil {
		return nil, grantOperationError()
	}
	for _, candidate := range p.candidates {
		if candidate.host.Name == name {
			if err := p.checkCandidateV1(ctx, candidate); err != nil {
				return nil, err
			}
			return candidate, nil
		}
	}
	return nil, autoPlacementNoHostV1()
}

func (p *AutoPlacementPlanV1) checkCandidateV1(ctx context.Context, candidate *autoPlacementCandidate) error {
	origin, err := readHostRuleOrigin(ctx, p.operation, p.options)
	if err != nil {
		return err
	}
	if origin.origin != p.origin || origin.originalCwd != p.options.OriginalCWD {
		return copyInputError("input_attribution", "the automatic-placement laptop policy origin changed")
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	file, err := loadHostsFile()
	if err != nil {
		return err
	}
	host, err := findPlacementHost(file.Hosts, candidate.host.Name)
	if err != nil || !SameEntry(host, candidate.host) {
		return grantIdentityError(candidate.host)
	}
	tiers, err := LoadTiersContext(ctx, file.Hosts)
	if err != nil {
		return err
	}
	if tiers.Of(host.Name) != candidate.tier || tiers.IDChanged(host.Name) {
		return grantIdentityError(host)
	}
	current, reason, err := p.localCandidateV1(ctx, settings, host, candidate.tier)
	if err != nil {
		return err
	}
	if reason == "" && candidate.level == "microvm" {
		reason, err = resolveAutoPlacementImageV1(ctx, current, candidate.selectionFactsV1())
	}
	if err != nil {
		return err
	}
	if reason != "" || !sameHostTransportSource(current.originalSource, candidate.originalSource) ||
		!reflect.DeepEqual(current.need, candidate.need) || !reflect.DeepEqual(current.localArgs, candidate.localArgs) || current.selectedWall != candidate.selectedWall {
		return grantIdentityError(candidate.host)
	}
	if candidate.observation != nil {
		return candidate.observation.check(ctx)
	}
	if candidate.cached != nil {
		return candidate.cached.check(ctx)
	}
	return grantOperationError()
}

// BindNewRequest deliberately keeps H19's genuine-observation requirement.
// Cache entries can select a wake; only fresh accepted facts can bind New.
func (p *AutoPlacementPlanV1) BindNewRequest(ctx context.Context, r *GrantRequest, name string) (context.Context, error) {
	candidate, err := p.candidateV1(ctx, name)
	if err != nil {
		return ctx, err
	}
	return p.selection.bindSelectedAutoNewRequestV1(ctx, r, candidate)
}

// Close joins every read-only contact before closing the advisory store.
// The caller keeps p on a cleanup deadline and may call Close again.
func (p *AutoPlacementPlanV1) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	var failures []error
	for _, owner := range p.dials {
		settled, err := owner.settle(ctx, nil)
		if !settled && err == nil {
			err = hostAttemptUncertainV1()
		}
		if !settled || err != nil {
			failures = append(failures, err)
			continue
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	if p.store != nil {
		if err := p.store.close(); err != nil {
			return err
		}
	}
	p.closed = true
	p.candidates, p.rows, p.dials, p.hosts = nil, nil, nil, nil
	return nil
}

func autoPlacementNoHostV1() error {
	return &UserError{Msg: "no automatic-placement host can take this sandbox",
		Hint: "Check new auto --dry-run, the host pool, host health and the requested tier and features.", Code: "no_host", Kind: KindRefused}
}

func clonePlacementNeedV1(need PlacementNeed) PlacementNeed {
	need.Features, need.RuntimeDigests = slices.Clone(need.Features), slices.Clone(need.RuntimeDigests)
	return need
}

func cloneAutoPlacementArgsV1(a NewArgs) NewArgs {
	a.Argv, a.Mounts, a.Include = slices.Clone(a.Argv), slices.Clone(a.Mounts), slices.Clone(a.Include)
	a.Layers = api.CloneLayers(a.Layers)
	a.SetupTrust, a.SetupHashes = maps.Clone(a.SetupTrust), maps.Clone(a.SetupHashes)
	return a
}

func unpreparedAutoPlacementArgsV1(a NewArgs) error {
	if a.ID != "" || a.HostLabel != "" || a.Placement != nil || a.Runtime != nil || len(a.VMNeedV1) != 0 ||
		a.Inputs != nil || a.Environment != nil || a.SecretDelivery != nil || len(a.Secrets) != 0 ||
		len(a.ProtectedTrustedSecretValues) != 0 || len(a.Layers) != 0 || a.RepositoryPolicy != nil ||
		a.GrantHash != "" || len(a.GrantAnswers) != 0 || len(a.SetupTrust) != 0 || len(a.SetupHashes) != 0 {
		return grantOperationError()
	}
	return nil
}

const autoPlacementFactsTimeoutV1 = 2500 * time.Millisecond
