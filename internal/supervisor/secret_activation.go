package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

const secretControlWait = 5 * time.Second

func requireBoundControl(ctx context.Context, owner *egressProc) error {
	if owner == nil || owner.ctl == nil || owner.dead.Load() || channelClosed(owner.ctl.done) {
		return secretIntentFailure()
	}
	var raw json.RawMessage
	if err := owner.ctl.request(ctx, egress.CtlCapabilities, nil, &raw); err != nil {
		return err
	}
	var capabilities egress.CapabilitiesResult
	if !api.ExactJSONFields(raw, capabilities) || json.Unmarshal(raw, &capabilities) != nil {
		owner.ctl.closeOwnedControl()
		return secretIntentFailure()
	}
	if !slices.Contains(capabilities.Features, api.FeatureBoundSecrets) || !slices.Contains(capabilities.Features, egress.MemberAddV2Feature) {
		return &proto.Error{Code: "helper_old", Message: "the running egress lacks bound-secret registry support",
			Hint: "Use matching helpers, then stop and resume the sandbox before passing bound values."}
	}
	return nil
}

func sameLivePrimary(a, b api.PublicSecretState) bool {
	return a.Mode == b.Mode && a.Sentinel == b.Sentinel && a.Identity == b.Identity && reflect.DeepEqual(a.Shape, b.Shape)
}

func (s *Supervisor) armSecrets(ctx context.Context, args api.ArmSecretsArgs) (*api.SessionSecretState, error) {
	root := s.owner()
	if !api.ValidSessionID(args.ID) {
		return nil, secretIntentFailure()
	}
	operation, err := host.BeginSessionOperation(ctx, args.ID, secretControlWait)
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	if operation.Primary() != root.id {
		return nil, secretIntentFailure()
	}
	root.polMu.Lock()
	defer root.polMu.Unlock()
	root.mu.Lock()
	if root.m == nil {
		root.mu.Unlock()
		return nil, secretIntentFailure()
	}
	owner := root.eg
	running := root.rec.phase == host.LifeRunning && !root.memberFault && ownedSecretControl(root, owner)
	snapshot := root.snapshot()
	root.mu.Unlock()
	if !running || snapshot.SecretState == nil || !snapshot.SecretState.Supported() || snapshot.SecretState.Generation != args.Generation {
		return nil, &proto.Error{Code: "secret_generation", Message: "the owned secret generation is unavailable or changed. Replacement did not occur",
			Hint: "Refresh the trusted session state and retry only the freshly approved bound names."}
	}
	if err := requireBoundControl(ctx, owner); err != nil {
		return nil, err
	}
	initial, err := owner.ctl.publicInitial(snapshot)
	if err != nil || egress.ValidatePublicSessionState(initial, snapshot.SecretState, root.publicEgressCA(owner)) != nil {
		return nil, secretIntentFailure()
	}
	if err := checkSecretPublicResponse(snapshot.SecretState); err != nil {
		return nil, err
	}
	if err := validateProtectedTrustedValues(args.TrustedUses, args.ProtectedTrustedSecretValues, true); err != nil {
		return nil, err
	}
	if err := requireTrustedJudgeControl(ctx, owner, args.TrustedUses); err != nil {
		return nil, err
	}
	expectedTrusted := overlayTrustedUses(snapshot.SecretState.Intent.TrustedUses, args.TrustedUses)
	names := slices.Sorted(maps.Keys(args.Secrets))
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind), TrustedUses: api.CloneTrustedSecretUses(args.TrustedUses)}
	for _, name := range names {
		if snapshot.SecretState.Intent.Modes[name] != api.SecretModeBound || snapshot.SecretState.Primary[name].Mode != api.SecretModeBound {
			return nil, secretIntentFailure()
		}
		intent.Modes[name] = api.SecretModeBound
		intent.Binds[name] = snapshot.SecretState.Intent.Binds[name]
	}
	if _, _, err := host.PrepareSecretDelivery(&intent, args.Secrets, true, recordedSecretNames(names)); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = owner.ctl.request(ctx, egress.CtlArm, egress.ArmArgs{Version: egress.MemberRegistryV2, Generation: args.Generation, Values: args.Secrets, TrustedUses: args.TrustedUses, ProtectedTrustedSecretValues: args.ProtectedTrustedSecretValues}, &raw)
	if err != nil {
		var negative *proto.Error
		if errors.As(err, &negative) {
			return nil, negative
		}
		owner.ctl.closeOwnedControl()
		return nil, secretCommitFailure()
	}
	var reply egress.ArmResult
	if !api.ExactJSONFields(raw, reply) || json.Unmarshal(raw, &reply) != nil || reply.Version != egress.MemberRegistryV2 || len(reply.Secrets) != len(names) || !egress.EqualTrustedSecretUsesV1(expectedTrusted, reply.TrustedUses) {
		owner.ctl.closeOwnedControl()
		return nil, secretCommitFailure()
	}
	for _, name := range names {
		public, exists := reply.Secrets[name]
		if !exists || !public.Armed || !sameLivePrimary(snapshot.SecretState.Primary[name], public) || egress.ValidatePublicSecretValue(ctx, name, public, intent.Binds[name], args.Secrets[name]) != nil {
			owner.ctl.closeOwnedControl()
			return nil, secretCommitFailure()
		}
	}
	generation := args.Generation
	state, err := root.commitSecretPublicState(ctx, owner, &generation, func(current *api.SessionSecretState) (*api.SessionSecretState, error) {
		if current == nil || !current.Supported() {
			return nil, secretCommitFailure()
		}
		for _, name := range names {
			if !sameLivePrimary(snapshot.SecretState.Primary[name], current.Primary[name]) || !sameSecretBinding(snapshot.SecretState.Intent.Binds[name], current.Intent.Binds[name]) {
				return nil, secretCommitFailure()
			}
			current.Primary[name] = reply.Secrets[name]
		}
		current.Intent.TrustedUses = api.CloneTrustedSecretUses(reply.TrustedUses)
		current.Generation = reply.Generation
		return current, nil
	})
	if err != nil {
		owner.ctl.closeOwnedControl()
		return nil, secretCommitFailure()
	}
	root.audit.write("secret_arm", map[string]any{"secrets": names})
	return state, nil
}

func (s *Supervisor) armRequest(c *client, request proto.Request) bool {
	if request.Op != "arm" {
		return false
	}
	if !c.full {
		c.reply(request.ID, nil, host.ErrInside)
		return true
	}
	var args api.ArmSecretsArgs
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil {
		c.reply(request.ID, nil, toProtoErr(secretIntentFailure()))
		return true
	}
	defer clear(args.Secrets)
	defer clear(args.ProtectedTrustedSecretValues)
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), secretControlWait)
	defer cancel()
	state, err := s.armSecrets(ctx, args)
	if err != nil {
		c.reply(request.ID, nil, toProtoErr(err))
	} else {
		c.reply(request.ID, state, nil)
	}
	return true
}
