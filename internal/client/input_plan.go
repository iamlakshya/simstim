package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

type inputPlanMode uint8

const (
	inputPlanNew inputPlanMode = iota + 1
	inputPlanWake
	inputPlanPendingCopy
)

// inputPlanSelection is constructed ONLY by H5 from the same approved original
// local creation snapshot/pin. It is a private view, not another settings
// reader or registry. Remote policy and rows cannot select laptop sources.
type inputPlanSelection struct {
	values          api.SandboxValues
	sourcePin       json.RawMessage
	originalCwd     string
	mode            inputPlanMode
	seeds           []string
	seedDirty       bool
	hostClone       bool
	hostRepository  string
	discovery       *inputSeedDiscovery
	primaryIncludes []string               // NEW-only local reviewed selection, not host include
	base            string                 // applies only to the primary source
	metadata        LocalGitMetadataOpener // original canonical settings/source lease factory
	resolvedEnv     map[string]string      // actual same-snapshot ordinary environment; non-nil empty is known empty
}

// SessionInputPlan owns a sealed local declaration and private source facts.
// Only the canonical client producer can publish its sources. A host row,
// caller confirmation or public struct literal cannot construct provenance.
type SessionInputPlan struct {
	mu        sync.Mutex
	closed    bool
	inUse     bool
	mode      inputPlanMode
	sourcePin json.RawMessage
	plan      api.SessionInputs
	summary   api.InputSummary
	env       map[string]string
	copyMax   int64
	seedMax   int64
	copies    map[string]plannedCopy
	seeds     map[string]*laptopSeed
	facts     []inputSourceFact
	seedOrder []*laptopSeed
	discovery *inputSeedDiscovery
}

func buildSessionInputPlan(ctx context.Context, selection inputPlanSelection) (_ *SessionInputPlan, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if len(selection.sourcePin) == 0 || !filepath.IsAbs(selection.originalCwd) ||
		selection.mode < inputPlanNew || selection.mode > inputPlanPendingCopy {
		return nil, &UserError{Code: "copy_plan_unknown", Msg: "the original approved local input plan is unavailable",
			Hint: "Use the client that created this session and restore its local policy record.", Kind: KindRefused}
	}
	if selection.mode != inputPlanNew && (len(selection.seeds) != 0 || selection.seedDirty) || selection.seedDirty && len(selection.seeds) == 0 {
		return nil, &UserError{Code: "flags_conflict", Msg: "--seed-dirty needs a laptop --seed on a new sandbox",
			Hint: "Pass --seed with --seed-dirty when creating the sandbox.", Kind: KindUsage}
	}
	if len(selection.seeds) != 0 && selection.discovery == nil ||
		selection.discovery != nil && (selection.discovery.originalCwd != selection.originalCwd || !slices.Equal(selection.discovery.requested, selection.seeds)) {
		return nil, &UserError{Code: "input_discovery", Msg: "the laptop primary repository was not checked before policy selection",
			Hint: "Prepare the source through the canonical local policy flow.", Kind: KindRefused}
	}
	// The sole original-snapshot producer resolves and validates this ordinary
	// environment before path reads. A missing source never becomes Settings{}.
	if selection.resolvedEnv == nil {
		return nil, gitMetadataFailure()
	}
	if selection.discovery.primary() != nil {
		if err := selection.discovery.revalidatePrimaryPolicy(ctx); err != nil {
			return nil, err
		}
	}
	env := maps.Clone(selection.resolvedEnv)
	copyMax, seedMax, err := api.InputLimits(selection.values)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, &UserError{Code: "copy_home", Msg: "the laptop home folder is unavailable",
			Hint: "Set a valid local home before preparing session inputs.", Kind: KindFailed}
	}
	var sources []CopySource
	for _, value := range selection.values.CopyIn {
		source, err := ParseCopySource(value, home, localWindows())
		if err != nil {
			return nil, err
		}
		source.Host = localInputPath(source.Host, selection.originalCwd)
		sources = append(sources, source)
	}
	copies, err := buildCopyPlan(ctx, sources, home, copyMax)
	if err != nil {
		return nil, err
	}
	if len(selection.primaryIncludes) != 0 {
		if selection.mode != inputPlanNew || selection.discovery.primary() == nil {
			return nil, copyInputError("include_scope", "laptop includes need a reviewed local primary on a new sandbox")
		}
		if err = copies.addReviewedIncludes(ctx, *selection.discovery.primary(), selection.primaryIncludes, copyMax); err != nil {
			return nil, err
		}
	}
	plan := &SessionInputPlan{mode: selection.mode, sourcePin: slices.Clone(selection.sourcePin), env: env,
		copyMax: copyMax, seedMax: seedMax, copies: map[string]plannedCopy{}, seeds: map[string]*laptopSeed{}, discovery: selection.discovery}
	defer func() {
		if err != nil {
			_ = plan.Close()
		}
	}()
	repositories := map[string]inputSourceFact{}
	for _, copy := range copies.files {
		plan.copies[copy.file.Path] = copy
		plan.plan.Files = append(plan.plan.Files, copy.file)
		var fact inputSourceFact
		if copy.include {
			primary := selection.discovery.primary()
			fact = inputSourceFact{kind: inputSourceRepository, origin: primary.origin, localRoot: primary.root, identity: primary.identity, include: true}
		} else {
			fact, err = copySourceProvenance(ctx, copy, repositories, selection.metadata)
			if err != nil {
				return nil, err
			}
		}
		plan.facts = append(plan.facts, fact)
	}
	// Discover every seed and duplicate basename BEFORE writing any object.
	var directories []string
	names := map[string]bool{}
	if selection.hostRepository != "" {
		names[selection.hostRepository] = true
	}
	var discovered []discoveredInputSeed
	if selection.discovery != nil {
		discovered = selection.discovery.seeds
	}
	for _, seed := range discovered {
		name := seed.name
		if !api.InputRepoName(name) || names[name] {
			return nil, &UserError{Code: "seed_duplicate", Msg: "two source folders have the same repository basename",
				Hint: "Use folders with distinct basenames before starting the sandbox.", Kind: KindUsage}
		}
		current, err := os.Stat(seed.root)
		if err != nil || !os.SameFile(current, seed.identity) {
			return nil, copyInputError("seed_changed", "the source repository changed after policy discovery")
		}
		names[name] = true
		directories = append(directories, seed.root)
	}
	if len(directories) != 0 || selection.hostClone {
		plan.plan.Identity, err = readCommandGitIdentity(ctx, selection.originalCwd, selection.metadata)
		if err != nil {
			return nil, err
		}
	}
	for index, directory := range directories {
		base := ""
		if index == 0 && selection.discovery.primary() != nil {
			base = selection.base
		}
		var selected *seedPolicySnapshot
		if index == 0 && selection.discovery.primary() != nil {
			selected = discovered[index].policy
		}
		seed, err := createLaptopSeedSelected(ctx, directory, selection.seedDirty, seedMax, plan.plan.Identity, base, selected, selection.metadata)
		if err != nil {
			return nil, err
		}
		plan.seeds[seed.declaration.Name] = seed
		plan.seedOrder = append(plan.seedOrder, seed)
		plan.plan.Repositories = append(plan.plan.Repositories, seed.declaration)
		fact, err := repositorySourceProvenance(ctx, directory, selection.metadata)
		if err != nil {
			return nil, err
		}
		if fact.origin != seed.declaration.Origin {
			return nil, copyInputError("seed_changed", "the source origin changed during capture")
		}
		plan.facts = append(plan.facts, fact)
	}
	plan.plan.Version = api.SessionInputsV1
	if plan.plan.Nonce, err = api.NewInputNonce(); err != nil {
		return nil, err
	}
	plan.plan = plan.plan.Canonical()
	plan.summary, err = plan.plan.Summary(copyMax, seedMax)
	if err != nil {
		return nil, err
	}
	// The actual New/Resume envelope is checked by the same producer too.
	if data, marshalErr := json.Marshal(plan.summary); marshalErr != nil || len(data)+1 > proto.MaxFrame {
		return nil, &UserError{Code: "inputs_too_big", Msg: "the input repository metadata exceeds the request bound",
			Hint: "Select fewer repository declarations for this start.", Kind: KindUsage}
	}
	return plan, ctx.Err()
}

// Summary returns an owned non-secret declaration. It contains no laptop
// planner/source path and carries no local permission to publish its bytes.
func (p *SessionInputPlan) Summary() (api.InputSummary, error) {
	if p == nil {
		return api.InputSummary{}, &UserError{Code: "inputs_closed", Msg: "the local input plan is unavailable", Hint: "Prepare the local inputs again.", Kind: KindRefused}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.summary.Nonce.Valid() {
		return api.InputSummary{}, &UserError{Code: "inputs_closed", Msg: "the local input plan is closed",
			Hint: "Prepare the local inputs again.", Kind: KindFailed}
	}
	result := p.summary
	result.Repositories = slices.Clone(result.Repositories)
	return result, nil
}

// Environment returns the already validated ordinary environment. The actual
// backend still applies protected session identity after every user layer.
func (p *SessionInputPlan) Environment() map[string]string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.env)
}

func (p *SessionInputPlan) withSources(ctx context.Context, publish func(api.SessionInputs, inputtransfer.Sources) error) error {
	p.mu.Lock()
	if p.closed || p.inUse || !p.summary.Nonce.Valid() || len(p.sourcePin) == 0 {
		p.mu.Unlock()
		return &UserError{Code: "inputs_busy", Msg: "the local input plan is closed or already publishing",
			Hint: "Wait for this input operation to finish before trying again.", Kind: KindRefused}
	}
	p.inUse = true
	plan := p.plan.Canonical()
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.inUse = false; p.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.discovery.primary() != nil {
		if err := p.discovery.revalidatePrimaryPolicy(ctx); err != nil {
			return err
		}
	}
	sources := inputtransfer.Sources{
		ValidatePayload: func(ctx context.Context) error { return p.discovery.revalidatePrimaryPolicy(ctx) },
		File: func(ctx context.Context, file api.InputFile, out io.Writer) error {
			copy, ok := p.copies[file.Path]
			if !ok || copy.file != file {
				return copyInputError("copy_changed", "the source no longer matches the sealed copy declaration")
			}
			return copy.write(ctx, out)
		},
		Repository: func(ctx context.Context, repository api.InputRepository, out io.Writer) error {
			seed := p.seeds[repository.Name]
			if seed == nil || seed.declaration != repository {
				return copyInputError("seed_changed", "the source no longer matches the sealed repository declaration")
			}
			return seed.write(ctx, out, p.seedMax)
		},
	}
	return publish(plan, sources)
}

// Close removes only this client's owned transient bundle captures. It never
// deletes a remote session, workspace, shared source repository or state.
func (p *SessionInputPlan) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inUse {
		return &UserError{Code: "inputs_busy", Msg: "the input operation still owns its local captures",
			Hint: "Cancel and join that operation before closing its plan.", Kind: KindRefused}
	}
	if p.closed {
		return nil
	}
	var failures []error
	for _, seed := range p.seedOrder {
		if err := seed.close(); err != nil {
			failures = append(failures, err)
		}
	}
	if p.discovery != nil {
		failures = append(failures, p.discovery.Close())
	}
	err := errors.Join(failures...)
	// A failed close keeps the plan retryable and retains each seed's actual
	// native cleanup owner. No input disappears beneath an unfinished pack.
	p.closed = err == nil
	return err
}
