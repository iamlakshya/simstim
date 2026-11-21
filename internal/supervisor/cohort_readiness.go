package supervisor

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) protectedCohortReadinessRequestV1(c *client, request proto.Request) bool {
	if request.Op != "info" || len(request.Args) == 0 {
		return false
	}
	var args api.SandboxCohortInfoArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil {
		c.reply(request.ID, nil, toProtoErr(secretIntentFailure()))
		return true
	}
	if args.TrustedReadiness == nil {
		return false
	}
	if !c.full || !args.Cohort {
		c.reply(request.ID, nil, host.ErrInside)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretControlWait)
	defer cancel()
	result, err := s.protectedCohortReadinessV1(ctx, args.TrustedReadiness)
	c.reply(request.ID, result, toProtoErr(err))
	return true
}

// The actual root's current control actor supplies armed state. A manifest,
// public badge, generation marker or matching tuple cannot replace that read.
func (s *Supervisor) protectedCohortReadinessV1(ctx context.Context, supplied *api.TrustedSecretReadinessQueryV1) (*api.SandboxCohortInfoResultV1, error) {
	query := api.CloneTrustedSecretReadinessQueryV1(supplied)
	if query == nil || query.Version != 1 || query.Generation == "" || len(query.Uses) == 0 || s.owner() != s {
		return nil, secretIntentFailure()
	}
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	if s.m == nil {
		s.mu.Unlock()
		return nil, secretIntentFailure()
	}
	actor := s.eg
	snapshot := s.snapshot()
	running := s.rec.phase == host.LifeRunning && !s.memberFault && ownedSecretControl(s, actor)
	s.mu.Unlock()
	if !running || snapshot.SecretState == nil || !snapshot.SecretState.Supported() || snapshot.SecretState.Generation != query.Generation || snapshot.Run.Generation == "" {
		return nil, secretIntentFailure()
	}
	// The private actor compares expected tuples with its CURRENT policy.
	// Historical public metadata may still describe an earlier unarmed role.
	if err := requireBoundControl(ctx, actor); err != nil {
		return nil, err
	}
	initial, err := actor.ctl.publicInitial(snapshot)
	if err != nil || egress.ValidatePublicSessionState(initial, snapshot.SecretState, s.publicEgressCA(actor)) != nil {
		return nil, secretIntentFailure()
	}
	var raw json.RawMessage
	if err := actor.ctl.request(ctx, egress.CtlTrustedReadiness, *query, &raw); err != nil {
		return nil, err
	}
	var readiness api.TrustedSecretReadinessV1
	if !api.ExactJSONFields(raw, readiness) || json.Unmarshal(raw, &readiness) != nil || !readiness.ValidFor(*query) {
		actor.ctl.closeOwnedControl()
		return nil, secretIntentFailure()
	}
	s.mu.Lock()
	valid := s.m != nil && s.eg == actor && s.rec.phase == host.LifeRunning && !s.memberFault && ownedSecretControl(s, actor) &&
		s.m.Run.Generation == snapshot.Run.Generation && s.m.Run.BootID == snapshot.Run.BootID && s.m.Run.StartedAt.Equal(snapshot.Run.StartedAt) &&
		s.m.SecretState != nil && s.m.SecretState.Generation == query.Generation && reflect.DeepEqual(s.m.SecretState.Intent.TrustedUses, snapshot.SecretState.Intent.TrustedUses)
	s.mu.Unlock()
	if !valid {
		return nil, secretIntentFailure()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return &api.SandboxCohortInfoResultV1{Current: encoded, TrustedReadiness: api.CloneTrustedSecretReadinessV1(&readiness)}, nil
}
