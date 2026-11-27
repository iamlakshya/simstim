package supervisor

import (
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureNamedSSHKeysV1) }

func (s *Supervisor) namedSSHKeyRequest(c *client, req proto.Request) bool {
	switch req.Op {
	case "ssh-key.status":
		fields, err := api.JSONObjectFields(req.Args)
		if err != nil || len(fields) != 0 {
			c.reply(req.ID, nil, toProtoErr(namedSSHError("invalid named SSH key status request")))
			return true
		}
		root := s.owner()
		root.mu.Lock()
		var next []string
		if root.m.Policy != nil {
			next = slices.Clone(root.m.Policy.Sandbox.SSHKeys)
		}
		root.mu.Unlock()
		slices.Sort(next)
		c.reply(req.ID, api.NamedSSHKeyViewV1{V: 1, Installed: root.namedSSHKeyNames(), NextStart: next}, nil)
	case "ssh-key.policy":
		var args api.NamedSSHKeyPolicyArgs
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.V != 1 || !s.namedSSHPolicySession(args.Session) || api.CheckSecretName(args.Name, false) != api.SecretNameOK {
			c.reply(req.ID, nil, toProtoErr(namedSSHError("invalid next-start SSH key selection")))
			return true
		}
		result, err := s.owner().changeNamedSSHKeyPolicy(args)
		c.reply(req.ID, result, toProtoErrNamedSSH(err))
	default:
		return false
	}
	return true
}

func (s *Supervisor) namedSSHPolicySession(selected string) bool {
	if selected == s.id {
		return true
	}
	primary, err := host.SandboxPrimaryID(selected)
	return err == nil && primary == s.owner().id
}

func toProtoErrNamedSSH(err error) *proto.Error {
	if err == nil {
		return nil
	}
	return &proto.Error{Code: api.CodeNamedSSHKeyRefused, Message: "the next-start SSH key selection did not complete", Hint: "Check the selected key, policy locks and supervisor log, then retry."}
}

func (s *Supervisor) changeNamedSSHKeyPolicy(args api.NamedSSHKeyPolicyArgs) (api.NamedSSHKeyPolicyResult, error) {
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	layers := api.CloneLayers(s.m.Layers)
	ep, ref := s.eg, sessionRefOf(s.m)
	ended := s.lifetime == nil || s.lifetime.Err() != nil || s.rec.phase != host.LifeRunning
	s.mu.Unlock()
	if ended {
		return api.NamedSSHKeyPolicyResult{}, namedSSHError("the sandbox no longer owns this policy change")
	}
	policy, layers, err := host.ApplyNamedSSHKeyPolicy(layers, args.Name, args.Allow)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	// Only names go to the canonical metadata writer. The installed agent is
	// deliberately untouched until a new original cold-start grant arrives.
	if err := pushPolicyToEgress(ep, ref, policy); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	s.mu.Lock()
	s.m.Policy, s.m.Layers = &policy, layers
	snapshot := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snapshot, false); err != nil {
		s.failPolicySave(ep)
		return api.NamedSSHKeyPolicyResult{}, err
	}
	names := slices.Clone(policy.Sandbox.SSHKeys)
	slices.Sort(names)
	s.audit.write("ssh_key_policy", map[string]any{"name": args.Name, "allow": args.Allow, "applies": "next_start"})
	return api.NamedSSHKeyPolicyResult{V: 1, Names: names, Applies: "next_start"}, nil
}
