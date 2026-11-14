package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

type memberPolicyFailure struct {
	preserve bool
	cause    error
}

func (e *memberPolicyFailure) Error() string {
	return "the member policy transaction was not established. Try: check the audit log, then stop and resume the sandbox"
}
func (e *memberPolicyFailure) Unwrap() error { return e.cause }
func preserveMember(err error) bool {
	var transaction *memberPolicyFailure
	return errors.As(err, &transaction) && transaction.preserve
}

func checkMemberCapability(ctx context.Context, ep *egressProc) error {
	if ep == nil || ep.dead.Load() || ep.ctl == nil {
		return execRefusal("the sandbox has no live member policy service")
	}
	var raw json.RawMessage
	if err := ep.ctl.request(ctx, egress.CtlCapabilities, nil, &raw); err != nil {
		return err
	}
	fields, err := api.JSONObjectFields(raw, "features")
	var result egress.CapabilitiesResult
	if err != nil || len(fields) != 1 || json.Unmarshal(raw, &result) != nil {
		ep.ctl.closeOwnedControl()
		return execRefusal("the member policy service sent an invalid capability reply")
	}
	if !slices.Contains(result.Features, egress.MemberAddV1Feature) {
		return execRefusal("the member policy service lacks member-add-v1")
	}
	return nil
}

// installMemberPolicy consumes H5-approved original-parent legacy exposures.
// V1 never manufactures binding/sentinel metadata or changes an old env.
func (s *Supervisor) installLegacyMemberPolicy(ctx context.Context, child *host.Manifest, approved map[string]api.Secret) (map[string]string, error) {
	root := s.owner()
	root.polMu.Lock()
	defer root.polMu.Unlock()
	root.mu.Lock()
	ep := root.eg
	root.mu.Unlock()
	if err := checkMemberCapability(ctx, ep); err != nil {
		return nil, err
	}
	kind := ""
	if row, known := agents.Find(child.Agent.Kind); known {
		kind = row.Kind
	}
	values := make(map[string]api.Secret, len(approved))
	for name, value := range approved {
		values[name] = value
	}
	finishKind := ep.ctl.beginPublicKind(kind) // BEFORE the actual first write
	retainKind := false
	defer func() { finishKind(retainKind) }()
	var raw json.RawMessage
	if err := ep.ctl.request(ctx, egress.CtlMemberAdd, egress.MemberAddArgs{Version: egress.MemberRegistryV1, Kind: kind, ExposedValues: values}, &raw); err != nil {
		var negative *proto.Error
		if errors.As(err, &negative) {
			return nil, &memberPolicyFailure{cause: negative}
		}
		ep.ctl.closeOwnedControl()
		retainKind = true // possible backend commit; no further success on dead link
		return nil, &memberPolicyFailure{preserve: true, cause: err}
	}
	fields, err := api.JSONObjectFields(raw, "version", "env")
	var result egress.MemberAddResult
	valid := err == nil && len(fields) == 2 && json.Unmarshal(raw, &result) == nil && result.Version == egress.MemberRegistryV1 && len(result.Env) == len(values)
	if valid {
		for name, value := range values {
			got, present := result.Env[name]
			if !present || got != value {
				valid = false
				break
			}
		}
	}
	if !valid {
		retainKind = true
		ep.ctl.closeOwnedControl()
		return nil, &memberPolicyFailure{preserve: true, cause: execRefusal("the committed member policy reply was invalid")}
	}
	retainKind = true
	out := make(map[string]string, len(values))
	names := make([]string, 0, len(values))
	for name, value := range result.Env {
		out[name] = string(value)
		names = append(names, name)
	}
	sort.Strings(names)
	// The backend member_add event is deliberately ignored by the old queue.
	// Record this one correlated names-only audit after its confirmed ACK.
	root.audit.write("member_add", map[string]any{"session": child.ID, "agent": kind, "secrets": names})
	return out, nil
}

func (s *Supervisor) sessionFeatures() []string {
	root := s.owner()
	root.mu.Lock()
	ready := root.memberReady
	bound := ready && root.m != nil && root.m.SecretState != nil && root.m.SecretState.Supported()
	root.mu.Unlock()
	features := slices.Clone(Features)
	if !ready {
		features = slices.DeleteFunc(features, func(feature string) bool { return feature == api.FeatureSpawn || feature == api.FeatureSandboxSnapshot })
	}
	if !bound {
		features = slices.DeleteFunc(features, func(feature string) bool { return feature == api.FeatureBoundSecrets })
	}
	return features
}
