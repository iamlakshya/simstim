package egress

import (
	"context"
	"net"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

func canonicalRegistryBinding(name string, input api.SecretBind) (api.SecretBind, error) {
	output, err := secretbind.Canonical(input)
	if err != nil {
		return api.SecretBind{}, memberError("bad_secret", "secret "+name+" has invalid binding metadata", "Use exact HTTPS authorities and a supported credential header/format.")
	}
	for _, authority := range output.Hosts {
		host, _, err := net.SplitHostPort(authority)
		if err != nil {
			return api.SecretBind{}, memberError("bad_secret", "secret "+name+" has invalid binding metadata", "Use exact HTTPS authorities.")
		}
		_, spliceOnly, err := httpClass(host)
		if err != nil || spliceOnly {
			return api.SecretBind{}, memberError("bad_secret", "secret "+name+" requires an inspectable destination", "Use a supported inspected HTTPS destination or an explicitly exposed tool.")
		}
	}
	return output, nil
}

func sameRegistryBinding(a, b api.SecretBind) bool {
	return a.Header == b.Header && a.Format == b.Format && slices.Equal(a.Hosts, b.Hosts)
}

// Prefix knowledge is local canonical data, never a host claim or learned
// private substring. Conflicting rows cannot donate another provider's prefix.
func registryPrefix(kind, name string) (string, error) {
	prefix, err := agents.SecretPrefixForAgent(kind, name)
	if err != nil {
		return "", memberError("bad_secret", "secret "+name+" has ambiguous canonical prefix metadata", "Use a distinct configured secret name or explicitly expose it.")
	}
	return prefix, nil
}

func publicBoundSecret(ctx context.Context, selectedKind, name string, value api.Secret, binding api.SecretBind) (*boundSecret, error) {
	prefix, err := registryPrefix(selectedKind, name)
	if err != nil {
		return nil, err
	}
	return publicBoundSecretWithPrefix(ctx, name, value, binding, prefix)
}

func publicBoundSecretWithPrefix(ctx context.Context, name string, value api.Secret, binding api.SecretBind, prefix string) (*boundSecret, error) {
	if !validBoundCredential(value) {
		return nil, memberError("bad_secret", "secret "+name+" is not a supported bound credential", "Return a nonempty credential without header control characters.")
	}
	kind, user, err := secretbind.ParseFormat(binding.Format)
	if err != nil {
		return nil, registryIntentError()
	}
	if field, valid := requestCredentialValue(kind, user, value); !valid || len(field)+len(binding.Header)+4 > maxHTTPHeaderBytes {
		return nil, memberError("secret_format", "secret "+name+" cannot use its HTTP credential format", "Use a representable credential format or explicitly expose a compatible tool.")
	}
	shape, rule, err := compileBoundShape(ctx, name, value, prefix, bindingPunctuationPolicy(binding))
	if err != nil {
		return nil, err
	}
	public, err := publicSecretShape(ctx, name, shape, rule)
	if err != nil {
		return nil, err
	}
	return &boundSecret{binding: binding, shape: shape, public: public, rule: rule}, nil
}

func bindingPunctuationPolicy(binding api.SecretBind) sentinelPunctuationPolicy {
	kind, _, _ := secretbind.ParseFormat(binding.Format)
	if kind == "bearer" {
		return sentinelPunctuationToken68
	}
	return sentinelPunctuationPrintable
}

func (state *memberState) hasBoundAuthority(authority string) bool {
	if state == nil || state.version != MemberRegistryV2 {
		return false
	}
	for _, secret := range state.secrets {
		if secret.bound != nil && slices.Contains(secret.bound.binding.Hosts, authority) {
			return true
		}
	}
	return false
}

func (s *Server) hasBoundDestination(host string, port int) bool {
	return port == 443 && s.currentMembers().hasBoundAuthority(net.JoinHostPort(host, "443"))
}

// Retired bindings require inspection for tripwires but remain absent from
// credential injection and response-mint authority.
func (s *Server) needsSecretInspection(host string, port int) bool {
	if port != 443 {
		return false
	}
	authority := net.JoinHostPort(host, "443")
	state := s.currentMembers()
	if state == nil || state.version != MemberRegistryV2 {
		return false
	}
	if state.hasBoundAuthority(authority) {
		return true
	}
	for _, entries := range state.retired {
		for _, entry := range entries {
			if slices.Contains(entry.binding.Hosts, authority) {
				return true
			}
		}
	}
	return false
}

func samePublicSecretShape(a, b *api.SecretShape) bool { return reflect.DeepEqual(a, b) }
