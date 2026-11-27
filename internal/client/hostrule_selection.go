package client

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// HostSelectionV1 owns local selection metadata and its original settings
// source. It issues no connection, grant, reservation or start authority.
type HostSelectionV1 struct {
	mu        sync.Mutex
	ctx       context.Context
	operation *settingsOperation
	selected  string
	options   NewInputOptions
	last      string
	origin    hostRuleOriginData
	plan      hostRulePlan
	ready     bool
	closed    bool
	closeErr  error
	stop      func() bool
}

// PrepareHostSelection captures the actual local settings before a dial. The
// real New owner still makes fresh, source-bound attempts for the chosen names.
func PrepareHostSelection(ctx context.Context, selected string, options NewInputOptions) (_ *HostSelectionV1, err error) {
	if ctx == nil {
		return nil, grantOperationError()
	}
	operation, err := openSettingsOperation(ctx)
	if err != nil {
		return nil, err
	}
	selection := &HostSelectionV1{ctx: ctx, operation: operation, selected: selected, options: cloneNewInputOptions(options)}
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, selection.Close())
		}
	}()
	origin, plan, err := selection.observe(ctx)
	if err != nil {
		return nil, err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return nil, err
	}
	selection.origin, selection.plan, selection.ready = origin, plan, true
	if origin.originalCwd != "" {
		selection.options.OriginalCWD = origin.originalCwd
	}
	selection.mu.Lock()
	selection.stop = context.AfterFunc(ctx, func() { _ = selection.Close() })
	selection.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep = true
	return selection, nil
}

// Names returns an owned ordered list. Names describe targets, not permission.
func (s *HostSelectionV1) Names() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx == nil || s.ctx.Err() != nil {
		return nil
	}
	return slices.Clone(s.plan.names)
}

// Automatic says the original request selected the existing auto-placement path.
func (s *HostSelectionV1) Automatic() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.ctx != nil && s.ctx.Err() == nil && s.plan.automatic
}

// Reason is safe local display text about what selected the target.
func (s *HostSelectionV1) Reason() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx == nil || s.ctx.Err() != nil {
		return ""
	}
	return s.plan.reason
}

// Defaulted is presentation metadata for the environment/saved-default branch.
// It never grants named-host status, pool eligibility or a source capability.
func (s *HostSelectionV1) Defaulted() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.ctx != nil && s.ctx.Err() == nil && s.plan.defaulted
}

// Check refuses changed selection inputs instead of silently picking another
// host. It may read protected local Git metadata. Call it before an attempt,
// outside HostLock and the first-write operation. New retains its own checks.
func (s *HostSelectionV1) Check(ctx context.Context) error {
	if s == nil || ctx == nil {
		return grantOperationError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		if err := s.ctx.Err(); err != nil {
			return err
		}
	}
	if s.closed || !s.ready || s.operation == nil || s.ctx == nil {
		return grantOperationError()
	}
	bounded, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	if err := s.operation.Revalidate(bounded); err != nil {
		return err
	}
	origin, plan, err := s.observe(bounded)
	if err != nil {
		return err
	}
	if origin != s.origin || plan.automatic != s.plan.automatic || plan.reason != s.plan.reason || plan.defaulted != s.plan.defaulted || !slices.Equal(plan.names, s.plan.names) {
		return copyInputError("input_attribution", "the local host-selection inputs changed after the request began")
	}
	return s.operation.Revalidate(bounded)
}

// Close joins any in-progress local check before closing the owned source.
func (s *HostSelectionV1) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	if s.operation != nil {
		s.closeErr = s.operation.Close()
		s.operation = nil
	}
	s.plan.names, s.options, s.origin = nil, NewInputOptions{}, hostRuleOriginData{}
	return s.closeErr
}

func (s *HostSelectionV1) observe(ctx context.Context) (hostRuleOriginData, hostRulePlan, error) {
	settings, err := s.operation.Settings(ctx)
	if err == nil {
		err = ValidateSettings(settings)
	}
	if err != nil {
		return hostRuleOriginData{}, hostRulePlan{}, err
	}
	var origin hostRuleOriginData
	if s.selected == "" {
		origin, err = readHostRuleOrigin(ctx, s.operation, s.options)
	} else {
		err = checkHostSelectionMetadata(ctx, s.operation, gitMetadataCheckKeys(localWindows()))
	}
	if err != nil {
		return origin, hostRulePlan{}, err
	}
	layers, err := ResolvePolicyLayers(settings, Host{}, origin.origin, s.options.Profile, s.options.Session)
	if err != nil {
		return origin, hostRulePlan{}, err
	}
	in := hostRulePlanInput{argument: s.selected, last: s.last}
	for _, layer := range layers {
		if layer.Sandbox.Host != nil {
			in.rule, in.ruleLayer, in.ruleName = layer.Sandbox.Host, layer.Kind, layer.Name
		}
	}
	file, err := loadHostsFile()
	if err != nil {
		return origin, hostRulePlan{}, err
	}
	tiers, err := LoadTiersContext(ctx, file.Hosts)
	if err != nil {
		return origin, hostRulePlan{}, err
	}
	if s.selected == "-" && !s.ready {
		if last, ok := LoadLast(); ok {
			s.last, in.last = last.Host, last.Host
		}
	}
	if s.selected == "" && in.rule == nil {
		key := brand.Env("DEFAULT_HOST")
		if err := checkHostSelectionMetadata(ctx, s.operation, []string{key}); err != nil {
			return origin, hostRulePlan{}, err
		}
		in.environment, in.saved = os.Getenv(key), file.Default
	}
	plan, err := resolveHostRulePlan(in, file.Hosts, tiers)
	return origin, plan, err
}

func checkHostSelectionMetadata(ctx context.Context, operation *settingsOperation, keys []string) (err error) {
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, metadata.Close()) }()
	if err := metadata.CheckKeys(ctx, keys); err != nil {
		return err
	}
	return metadata.Revalidate(ctx)
}
