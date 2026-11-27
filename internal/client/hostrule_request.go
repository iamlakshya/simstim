package client

import (
	"context"
	"slices"
)

// BindNewRequest fixes one named attempt before its dial. The selection is an
// actual owned object, not a callback that could approve another request.
func (s *HostSelectionV1) BindNewRequest(ctx context.Context, r *GrantRequest, name string) (context.Context, error) {
	if err := s.Check(ctx); err != nil {
		return ctx, err
	}
	s.mu.Lock()
	named := !s.closed && !s.plan.automatic && slices.Contains(s.plan.names, name)
	s.mu.Unlock()
	if !named {
		return ctx, grantOperationError()
	}
	return s.bindSelectionRequestV1(ctx, r, name)
}

// Only the actual pool/need planner can supply this private concrete candidate.
// Its observation is checked again. Auto metadata alone cannot name a host.
func (s *HostSelectionV1) bindSelectedAutoNewRequestV1(ctx context.Context, r *GrantRequest, candidate *autoPlacementCandidate) (context.Context, error) {
	if candidate == nil || candidate.observation == nil {
		return ctx, grantOperationError()
	}
	if err := s.Check(ctx); err != nil {
		return ctx, err
	}
	if !s.Automatic() {
		return ctx, grantOperationError()
	}
	if err := candidate.observation.check(ctx); err != nil {
		return ctx, err
	}
	if err := validatePlacementNeed(candidate.need); err != nil {
		return ctx, err
	}
	if !SameEntry(candidate.host, candidate.observation.host) {
		return ctx, grantIdentityError(candidate.host)
	}
	file, err := loadHostsFile()
	if err != nil {
		return ctx, err
	}
	current, err := findPlacementHost(file.Hosts, candidate.host.Name)
	if err != nil || !SameEntry(current, candidate.host) {
		return ctx, grantIdentityError(candidate.host)
	}
	tiers, err := LoadTiersContext(ctx, file.Hosts)
	if err != nil {
		return ctx, err
	}
	reason, err := poolPlacementReason(current, tiers.Of(current.Name), candidate.need)
	if err != nil {
		return ctx, err
	}
	if reason != "" {
		return ctx, &UserError{Msg: "the selected host no longer passes the local automatic-placement filter: " + reason,
			Hint: "Run automatic placement again with the current host pool and request.", Code: "no_host", Kind: KindRefused}
	}
	return s.bindSelectionRequestV1(ctx, r, current.Name)
}

func (s *HostSelectionV1) bindSelectionRequestV1(ctx context.Context, r *GrantRequest, name string) (context.Context, error) {
	if ctx == nil || r == nil || !ValidHostName(name) {
		return ctx, grantOperationError()
	}
	if before := grantRequestOf(ctx); before != nil && before != r {
		return ctx, grantOperationError()
	}
	if err := s.checkOriginalHostSelectionSourceV1(ctx); err != nil {
		return ctx, err
	}
	r.mu.Lock()
	if r.hostSelection != nil || r.selectedHost != "" {
		same := r.hostSelection == s && r.selectedHost == name && grantRequestOf(ctx) == r &&
			r.purpose == grantRemote && r.operation == grantOpNew && r.operationUntrusted == r.Untrusted
		err := r.lifetimeErrorLocked()
		r.mu.Unlock()
		if !same {
			return ctx, grantOperationError()
		}
		return ctx, err
	}
	if !freshHostSelectionRequestV1(r) || r.requestContext != nil && grantRequestOf(ctx) != r {
		r.mu.Unlock()
		return ctx, grantOperationError()
	}
	r.mu.Unlock()
	if grantRequestOf(ctx) == nil {
		ctx = WithGrantRequest(ctx, r)
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return ctx, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return ctx, err
	}
	if !freshHostSelectionRequestV1(r) || r.hostSelection != nil || r.selectedHost != "" {
		return ctx, grantOperationError()
	}
	r.hostSelection, r.selectedHost = s, name
	return ctx, nil
}

// Caller holds r.mu. No earlier transport, answer, value or operation may be
// donated to a newly named attempt. A wall declaration alone is still intent.
func freshHostSelectionRequestV1(r *GrantRequest) bool {
	return r.purpose == grantRemote && !r.ending && !r.preparationClosed &&
		(r.operation == grantOpUnset || r.operation == grantOpNew) &&
		r.selection == nil && r.newIntent == nil && r.newSecrets == nil && r.newInputs == nil &&
		r.preparationSettings == nil && r.preparedVM == nil && r.freshChild == nil && r.resume == nil &&
		r.legacyResume == nil && r.join == nil && r.review == nil && r.push == nil && r.root == nil &&
		r.rootExecution == nil && r.forge == nil && r.metadataMode == nil &&
		r.imageSave == nil && r.id == "" && r.token == "" && freshAutoPlacementWakeAnswersV1(r) && len(r.recorded) == 0
}

// This is the cheap physical source check. It runs no Git, environment lookup,
// profile resolution, pool ranking or dial inside the actual writer's guard.
func (s *HostSelectionV1) checkOriginalHostSelectionSourceV1(ctx context.Context) error {
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
	return s.operation.Revalidate(ctx)
}

// Existing callers without a host-selection facade keep their existing
// source checks. Call this hook outside an already-held r.mu.
func checkHostSelectionPublicationV1(ctx context.Context, conn *Conn) error {
	if ctx == nil {
		return grantOperationError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	selection, name := r.hostSelection, r.selectedHost
	if selection == nil {
		r.mu.Unlock()
		if name != "" {
			return grantOperationError()
		}
		return nil
	}
	err := r.lifetimeErrorLocked()
	valid := r.purpose == grantRemote && r.operation == grantOpNew && r.operationUntrusted == r.Untrusted
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if !valid || conn == nil || name == "" || conn.Host.Name != name {
		return grantOperationError()
	}
	if err := checkAcceptedHostTransport(conn); err != nil {
		return err
	}
	return selection.checkOriginalHostSelectionSourceV1(ctx)
}
