package egress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type boundSecret struct {
	binding  api.SecretBind
	shape    sentinelShape
	public   *api.SecretShape
	rule     string
	sentinel api.Secret
}

type registeredMint struct {
	value       api.Secret
	sentinel    api.Secret
	shape       sentinelShape
	rule        string
	authority   string
	identity    string
	ready       bool
	retired     bool
	publication *mintPublication
}

func (boundSecret) Format(f fmt.State, _ rune)    { _, _ = io.WriteString(f, api.Redacted) }
func (registeredMint) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func registryNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", memberError("secret_state", "cannot prepare a secret generation", "Retry the approved operation or start a fresh sandbox.")
	}
	return hex.EncodeToString(value[:]), nil
}

func cloneSentinelShape(input sentinelShape) sentinelShape {
	return sentinelShape{Prefix: input.Prefix, Slots: slices.Clone(input.Slots)}
}

func cloneMemberState(input *memberState) (*memberState, error) {
	raw, err := json.Marshal(input.metadata)
	if err != nil {
		return nil, memberError("bad_request", "invalid egress state metadata", "Retry with matching helpers.")
	}
	var metadata InitArgs
	if json.Unmarshal(raw, &metadata) != nil {
		return nil, memberError("bad_request", "invalid egress state metadata", "Retry with matching helpers.")
	}
	output := &memberState{version: MemberRegistryV2, metadata: metadata, kinds: slices.Clone(input.kinds), generation: input.generation, secrets: make(map[string]registeredSecret, len(input.secrets)), sourceKinds: cloneStartupSources(input.sourceKinds), retired: cloneRetiredPrimaries(input.retired), decoys: api.CloneDecoyTripwires(input.decoys), decoyClasses: slices.Clone(input.decoyClasses)}
	for name, old := range input.secrets {
		secret := old
		if old.trusted != nil {
			use := api.CloneTrustedSecretUses(map[string]api.TrustedSecretUseV1{name: *old.trusted})[name]
			secret.trusted = &use
		}
		secret.history = slices.Clone(old.history)
		secret.minted = slices.Clone(old.minted)
		for i := range secret.minted {
			secret.minted[i].shape = cloneSentinelShape(secret.minted[i].shape)
		}
		if old.bound != nil {
			binding := *old.bound
			binding.binding.Hosts = slices.Clone(binding.binding.Hosts)
			binding.shape = cloneSentinelShape(binding.shape)
			if binding.public != nil {
				public := *binding.public
				public.Slots = slices.Clone(public.Slots)
				binding.public = &public
			}
			secret.bound = &binding
		}
		if input.version == MemberRegistryV1 {
			secret.mode, secret.present = api.SecretModeExposed, true
		}
		output.secrets[name] = secret
	}
	return output, nil
}

func (state *memberState) publicSecrets(names []string) map[string]api.PublicSecretState {
	output := make(map[string]api.PublicSecretState, len(names))
	for _, name := range names {
		secret, exists := state.secrets[name]
		if !exists || !secretHasGuestUse(secret) {
			continue
		}
		public := api.PublicSecretState{Mode: secret.mode, Armed: secret.present}
		if secret.bound != nil {
			public.Sentinel, public.Shape, public.Identity = secret.bound.sentinel, secret.bound.public, secret.identity
		}
		output[name] = public
	}
	return api.ClonePublicSecretStates(output)
}

// This internal projection is ONLY used with the real control writer's size
// check. It must never be returned, logged, persisted or sent to a peer.
type registrySizeSecret struct {
	Trusted      *api.TrustedSecretUseV1 `json:"trusted_use,omitempty"`
	TrustedArmed bool                    `json:"trusted_armed,omitempty"`
	Guest        bool                    `json:"guest"`
	Mode         api.SecretMode          `json:"mode"`
	Present      bool                    `json:"present"`
	Value        api.Secret              `json:"value"`
	Identity     string                  `json:"identity"`
	ValueEpoch   string                  `json:"value_epoch"`
	Binding      *api.SecretBind         `json:"binding,omitempty"`
	Public       *api.PublicSecretState  `json:"public,omitempty"`
	History      []api.Secret            `json:"history,omitempty"`
	Minted       []registrySizeMint      `json:"minted,omitempty"`
}

type registrySizeMint struct {
	Value, Sentinel                            api.Secret
	Shape                                      *api.SecretShape
	Rule, Authority                            string
	Identity                                   string
	Ready, Retired                             bool
	Receipt, ReservedGeneration, ReservedEpoch string
}

func (registrySizeSecret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (registrySizeMint) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }

func checkBoundStateSize(ctx context.Context, state *memberState) error {
	_, err := boundStateFrameBytes(ctx, state)
	return err
}

func boundStateFrameBytes(ctx context.Context, state *memberState) (int, error) {
	entries := make(map[string]registrySizeSecret, len(state.secrets))
	for name, secret := range state.secrets {
		entry := registrySizeSecret{Trusted: secret.trusted, TrustedArmed: secret.trustedArmed, Guest: secretHasGuestUse(secret), Mode: secret.mode, Present: secret.present, Value: secret.value, Identity: secret.identity, ValueEpoch: secret.valueEpoch, History: secret.history}
		if secret.bound != nil {
			binding := secret.bound.binding
			entry.Binding = &binding
			public := state.publicSecrets([]string{name})[name]
			entry.Public = &public
		}
		for _, mint := range secret.minted {
			shape, err := publicSecretShape(ctx, name, mint.shape, mint.rule)
			if err != nil {
				return 0, err
			}
			row := registrySizeMint{Value: mint.value, Sentinel: mint.sentinel, Shape: shape, Rule: mint.rule, Authority: mint.authority, Identity: mint.identity, Ready: mint.ready, Retired: mint.retired}
			if mint.publication != nil {
				row.Receipt, row.ReservedGeneration, row.ReservedEpoch = mint.publication.receipt, mint.publication.generation, mint.publication.epoch
			}
			entry.Minted = append(entry.Minted, row)
		}
		entries[name] = entry
	}
	retired, err := publicRetiredPrimaries(ctx, state.retired)
	if err != nil {
		return 0, err
	}
	projection := struct {
		Version     int                                        `json:"version"`
		Initial     InitArgs                                   `json:"initial"`
		Generation  string                                     `json:"generation"`
		SourceKinds map[string][]string                        `json:"source_kinds,omitempty"`
		Retired     map[string][]api.RetiredPrimarySecretState `json:"retired,omitempty"`
		Entries     map[string]registrySizeSecret              `json:"entries"`
	}{MemberRegistryV2, state.metadata, state.generation, state.sourceKinds, retired, entries}
	raw, err := json.Marshal(projection)
	if err != nil {
		return 0, memberError("bad_request", "cannot prepare complete secret state", "Retry with matching helpers.")
	}
	counter := new(privateFrameCounter)
	if proto.NewWriter(counter).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlBoundInit, Args: raw}) != nil {
		return 0, memberError("state_too_large", "complete secret state exceeds the control frame limit; replacement did not occur", "Stop and resume the sandbox with only the required approved names and policy.")
	}
	return counter.bytes - 4, nil
}
