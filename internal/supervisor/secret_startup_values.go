package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

// startupSecretEnvelope is the same existing protected startup message, with
// primary intent beside each member's canonical typed intent field.
type startupSecretEnvelope struct {
	ProtectedTrustedSecretValues map[string]api.Secret     `json:"protected_trusted_secret_values,omitempty"`
	NamedSSHKeys                 *api.NamedSSHKeysV1       `json:"named_ssh_keys,omitempty"`
	Env                          map[string]api.Secret     `json:"env"`
	Members                      []api.MemberResumeArgs    `json:"members"`
	AllowMissing                 bool                      `json:"allow_missing"`
	SecretDelivery               *api.SecretDeliveryIntent `json:"secret_delivery,omitempty"`
}

func (startupSecretEnvelope) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

type processSecretInput struct {
	Names    []string
	Intent   api.SecretDeliveryIntent
	Supplied map[string]bool
}

type startupSecretInput struct {
	ProtectedTrustedSecretValues map[string]api.Secret
	Intent                       api.SecretDeliveryIntent
	Values                       map[string]api.Secret
	SourceKinds                  map[string][]string
	Processes                    map[string]processSecretInput
	Environments                 map[string]secretEnvironment
}

func (startupSecretInput) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func recordedSecretNames(names []string) []string {
	owned := slices.Clone(names)
	if owned == nil {
		owned = []string{}
	}
	return owned
}

func selectedSecretKind(kind string) string {
	if row, known := agents.Find(kind); known {
		return row.Kind
	}
	return ""
}

// validateStartupSecrets is the narrow shared prestart-reader hook. It never
// accepts a connection itself and commits no partial member/value plan.
func (s *Supervisor) validateStartupSecrets(ctx context.Context, raw []byte, want []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var message startupSecretEnvelope
	if json.Unmarshal(raw, &message) != nil {
		return nil, secretIntentFailure()
	}
	if message.NamedSSHKeys != nil {
		defer clear(message.NamedSSHKeys.Values)
	}
	defer clear(message.ProtectedTrustedSecretValues)
	defer func() {
		for _, member := range message.Members {
			clear(member.ProtectedTrustedSecretValues)
		}
	}()
	if err := s.checkOriginalPrivateStartupPayloadV1(ctx, raw, message); err != nil {
		return nil, err
	}
	s.mu.Lock()
	root := s.snapshot()
	s.mu.Unlock()
	if err := knownSessionSecretState(root.SecretState); err != nil {
		return nil, err
	}
	modern := root.SecretState != nil || message.SecretDelivery != nil || len(message.ProtectedTrustedSecretValues) > 0
	for _, member := range s.knownMembers {
		if err := knownSessionSecretState(member.SecretState); err != nil {
			return nil, err
		}
		modern = modern || member.SecretState != nil
	}
	for _, member := range message.Members {
		modern = modern || member.SecretDelivery != nil || len(member.ProtectedTrustedSecretValues) > 0
	}
	if (modern || message.NamedSSHKeys != nil) && !api.ExactJSONFields(raw, startupSecretEnvelope{}) {
		return nil, secretIntentFailure()
	}
	if err := s.checkStartupNamedSSHKeys(root, message); err != nil {
		return nil, err
	}
	var plan *startupSecretInput
	var memberValues map[string]map[string]string
	if modern {
		var err error
		plan, err = prepareStartupSecretInput(ctx, root, s.savedMembers, message)
		if err != nil {
			return nil, err
		}
	} else {
		checked, err := host.CheckResumeMemberValues(root.ID, s.savedMembers, message.Members)
		if err != nil {
			return nil, err
		}
		memberValues = make(map[string]map[string]string, len(checked))
		for id, values := range checked {
			memberValues[id] = make(map[string]string, len(values))
			for name, value := range values {
				memberValues[id][name] = string(value)
			}
		}
	}
	primary := make(map[string]string, len(want))
	for _, name := range want {
		value, supplied := message.Env[name]
		if supplied {
			primary[name] = string(value)
		} else if !message.AllowMissing {
			return nil, secretIntentFailure()
		}
	}
	s.mu.Lock()
	s.startupSecrets, s.memberValues = plan, memberValues
	s.startupNamedSSHKeys = cloneStartupNamedSSHKeys(message.NamedSSHKeys)
	s.m.Run.StartEnvelope = false
	s.mu.Unlock()
	return primary, nil
}

// prepareStartupSecretInput validates the COMPLETE original member bundle
// before constructing one ephemeral initial value payload. No caller fields
// or public state can nominate additional selected commands or fetches.
func prepareStartupSecretInput(ctx context.Context, root host.Manifest, members []*host.Manifest, message startupSecretEnvelope) (*startupSecretInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := knownSessionSecretState(root.SecretState); err != nil {
		return nil, err
	}
	checked, err := host.CheckResumeMemberValues(root.ID, members, message.Members)
	if err != nil {
		return nil, err
	}
	plan := &startupSecretInput{Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)},
		Values: make(map[string]api.Secret), SourceKinds: make(map[string][]string), Processes: make(map[string]processSecretInput)}
	complete := false
	defer func() {
		if !complete {
			clear(plan.Values)
			clear(plan.ProtectedTrustedSecretValues)
		}
	}()
	if err := plan.addProcess(ctx, root, message.SecretDelivery, message.Env, message.ProtectedTrustedSecretValues); err != nil {
		return nil, err
	}
	if !message.AllowMissing {
		for _, name := range plan.Processes[root.ID].Names {
			if !plan.Processes[root.ID].Supplied[name] {
				return nil, secretIntentFailure()
			}
		}
	}
	byID := make(map[string]api.MemberResumeArgs, len(message.Members))
	for _, member := range message.Members {
		byID[member.ID] = member
	}
	for _, member := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if member.ID == root.ID || member.SandboxOf != root.ID || !api.ValidSessionID(member.ID) {
			return nil, secretIntentFailure()
		}
		if err := knownSessionSecretState(member.SecretState); err != nil {
			return nil, err
		}
		if err := plan.addProcess(ctx, *member, byID[member.ID].SecretDelivery, checked[member.ID], byID[member.ID].ProtectedTrustedSecretValues); err != nil {
			return nil, err
		}
	}
	for name, kinds := range plan.SourceKinds {
		if _, fresh := plan.Values[name]; !fresh {
			delete(plan.SourceKinds, name)
			continue
		}
		slices.Sort(kinds)
		plan.SourceKinds[name] = slices.Compact(kinds)
	}
	if len(plan.SourceKinds) == 0 {
		plan.SourceKinds = nil
	}
	complete = true
	return plan, nil
}

func (p *startupSecretInput) addProcess(ctx context.Context, manifest host.Manifest, intent *api.SecretDeliveryIntent, values map[string]api.Secret, protectedTrusted ...map[string]api.Secret) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if intent == nil {
		return secretIntentFailure()
	}
	owned, names, err := host.PrepareSecretDelivery(intent, values, true, recordedSecretNames(manifest.Secrets))
	if err != nil {
		return err
	}
	if len(protectedTrusted) > 1 {
		return secretIntentFailure()
	}
	var protected map[string]api.Secret
	if len(protectedTrusted) == 1 {
		protected = protectedTrusted[0]
	}
	if err := p.addTrustedProcess(ctx, *owned, protected, values); err != nil {
		return err
	}
	process := processSecretInput{Names: names, Intent: *owned, Supplied: make(map[string]bool, len(values))}
	kind := selectedSecretKind(manifest.Agent.Kind)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := owned.Modes[name]
		if old, exists := p.Intent.Modes[name]; exists {
			if old != mode || mode == api.SecretModeBound && !sameSecretBinding(p.Intent.Binds[name], owned.Binds[name]) {
				return secretIntentFailure()
			}
		} else {
			p.Intent.Modes[name] = mode
			if mode == api.SecretModeBound {
				p.Intent.Binds[name] = owned.Binds[name]
			}
		}
		if mode == api.SecretModeBound {
			// Every actual selected claimant constrains a shared fresh
			// name, even when another requested member supplied its value.
			p.SourceKinds[name] = append(p.SourceKinds[name], kind)
		}
		value, supplied := values[name]
		if !supplied {
			continue
		}
		process.Supplied[name] = true
		if old, exists := p.Values[name]; exists && old != value {
			return secretIntentFailure()
		}
		if trusted, exists := p.ProtectedTrustedSecretValues[name]; exists && trusted != value {
			return secretIntentFailure()
		}
		p.Values[name] = value
	}
	p.Processes[manifest.ID] = process
	return nil
}
