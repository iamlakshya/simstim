package supervisor

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func secretNextStartAuthority() error {
	return &proto.Error{Code: "secret_intent", Message: "the next start cannot change an existing secret mode or binding without new local authority",
		Hint: "Start an explicitly approved new sandbox for a binding or exposure change. Saved public state grants no new permission."}
}

func missingBoundShape() error {
	return &proto.Error{Code: "secret_missing", Message: "a bound name has neither a fresh value nor a saved public shape",
		Hint: "Supply its freshly approved value before starting. Do not expose it or guess a stand-in format."}
}

// boundStartupArgs separates actual fresh publication from cold public-only
// restore. Fresh value-only shape changes retire old descriptors explicitly.
func boundStartupArgs(ctx context.Context, initial egress.InitArgs, retained *api.SessionSecretState, fresh *startupSecretInput, intentOnly *api.SecretDeliveryIntent) (egress.BoundInitArgs, error) {
	initial.Policy = api.ClonePolicy(initial.Policy)
	initial.AgentKinds = slices.Clone(initial.AgentKinds)
	args := egress.BoundInitArgs{Version: egress.MemberRegistryV2, Initial: initial, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind), Saved: make(map[string]api.PublicSecretState)}
	args.Initial.Secrets = nil
	if retained != nil {
		if err := egress.ValidateSavedPublicSessionState(retained); err != nil {
			return egress.BoundInitArgs{}, err
		}
		args.Modes = maps.Clone(retained.Intent.Modes)
		args.Binds = api.CloneSecretBinds(retained.Intent.Binds)
		if args.Modes == nil {
			args.Modes = make(map[string]api.SecretMode)
		}
		if args.Binds == nil {
			args.Binds = make(map[string]api.SecretBind)
		}
		args.SavedMinted = api.ClonePublicMintedStates(retained.Minted)
		args.SavedRetired = api.CloneRetiredPrimaryStates(retained.Retired)
	}
	if fresh != nil {
		if err := overlayStartupIntent(args.Modes, args.Binds, fresh.Intent); err != nil {
			return egress.BoundInitArgs{}, err
		}
		args.Initial.Secrets = make(map[string]string, len(fresh.Values))
		for name, value := range fresh.Values {
			args.Initial.Secrets[name] = string(value)
		}
		args.SourceKinds = make(map[string][]string, len(fresh.SourceKinds))
		for name, kinds := range fresh.SourceKinds {
			args.SourceKinds[name] = slices.Clone(kinds)
		}
	} else if intentOnly != nil {
		owned, _, err := host.PrepareSecretDelivery(intentOnly, nil, true, nil)
		if err != nil {
			return egress.BoundInitArgs{}, err
		}
		if err := overlayStartupIntent(args.Modes, args.Binds, *owned); err != nil {
			return egress.BoundInitArgs{}, err
		}
	}
	retired := &api.SessionSecretState{Retired: args.SavedRetired}
	for name, mode := range args.Modes {
		if err := ctx.Err(); err != nil {
			return egress.BoundInitArgs{}, err
		}
		if mode != api.SecretModeBound {
			continue
		}
		var old api.PublicSecretState
		var saved bool
		if retained != nil {
			old, saved = retained.Primary[name]
		}
		_, supplied := args.Initial.Secrets[name]
		if supplied {
			if saved && old.Mode == api.SecretModeBound {
				entry := api.RetiredPrimarySecretState{Sentinel: old.Sentinel, Shape: old.Shape, Identity: old.Identity, Binding: retained.Intent.Binds[name]}
				if err := appendRetiredPublic(retired, name, entry); err != nil {
					return egress.BoundInitArgs{}, err
				}
			}
			// Current Saved is intentionally omitted for a genuinely fresh
			// same-authority value. Its actual shape may change next boot.
			continue
		}
		if !saved || old.Mode != api.SecretModeBound {
			return egress.BoundInitArgs{}, missingBoundShape()
		}
		args.Saved[name] = api.ClonePublicSecretStates(map[string]api.PublicSecretState{name: old})[name]
	}
	args.SavedRetired = retired.Retired
	return args, nil
}

// coldStartupIntent retains original declared missing names too. A genuine
// legacy record means exposed-only metadata, never an inferred bound value.
func coldStartupIntent(root host.Manifest, members []*host.Manifest, retained *api.SessionSecretState) (*api.SecretDeliveryIntent, error) {
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)}
	if retained != nil {
		intent = api.CloneSecretDeliveryIntent(retained.Intent)
	}
	if intent.Modes == nil {
		intent.Modes = make(map[string]api.SecretMode)
	}
	if intent.Binds == nil {
		intent.Binds = make(map[string]api.SecretBind)
	}
	for _, record := range append([]*host.Manifest{&root}, members...) {
		if record.SecretState == nil {
			for _, name := range record.Secrets {
				if _, exists := intent.Modes[name]; !exists {
					intent.Modes[name] = api.SecretModeExposed
				}
			}
			continue
		}
		if err := knownSessionSecretState(record.SecretState); err != nil {
			return nil, err
		}
		if hasSavedSecretRecords(record.SecretState) {
			continue // the canonical complete collector already owns these
		}
		owned, _, err := host.PrepareSecretDelivery(&record.SecretState.Intent, nil, true, recordedSecretNames(record.Secrets))
		if err != nil {
			return nil, err
		}
		for name, mode := range owned.Modes {
			if _, exists := intent.Modes[name]; exists {
				continue // current canonical root metadata wins
			}
			intent.Modes[name] = mode
			if mode == api.SecretModeBound {
				intent.Binds[name] = owned.Binds[name]
			}
		}
	}
	return &intent, nil
}

func overlayStartupIntent(modes map[string]api.SecretMode, binds map[string]api.SecretBind, incoming api.SecretDeliveryIntent) error {
	if incoming.Version != api.SecretDeliveryIntentV1 {
		return secretIntentFailure()
	}
	for name, mode := range incoming.Modes {
		if !mode.Valid() {
			return secretIntentFailure()
		}
		if old, exists := modes[name]; exists && (old != mode || mode == api.SecretModeBound && !sameSecretBinding(binds[name], incoming.Binds[name])) {
			return secretNextStartAuthority()
		}
		modes[name] = mode
		if mode == api.SecretModeBound {
			binds[name] = api.CloneSecretBinds(map[string]api.SecretBind{name: incoming.Binds[name]})[name]
		}
	}
	return nil
}
