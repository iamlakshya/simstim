package client

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func originalNamedSSHPolicyV1(ctx context.Context) *namedSSHKeyPolicyPlanV1 {
	if r := grantRequestOf(ctx); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.namedPolicy
	}
	return nil
}

// Only the original local producer can send a private named-key carrier or a
// native resume continuation. Generic Call must not turn a DTO into either.
func beforeNamedSSHKeysRequestV1(ctx context.Context, c *Conn, request proto.Request, args any) error {
	if request.Op == "ssh-key.policy" {
		return originalNamedSSHPolicyV1(ctx).beforeRequest(ctx, c, request)
	}
	if request.Op == "resume" {
		if named, _ := ctx.Value(resumeNamedSSHKeyPublicationKey{}).(*resumeNamedSSHKeyPlan); named != nil {
			return named.beforeRequest(ctx, c, request, args)
		}
	}
	if request.Op == api.OpRewindCommitV1 {
		if named, _ := ctx.Value(resumeNamedSSHKeyPublicationKey{}).(*resumeNamedSSHKeyPlan); named != nil {
			return named.beforeRequest(ctx, c, request, args)
		}
		if rewind := originalRewindV1(ctx); rewind != nil && rewind.named == nil {
			return nil
		}
		return namedSSHKeySourceError()
	}
	if request.Op != "new" && request.Op != "resume" {
		return nil
	}
	if request.Op == "new" {
		if witness, _ := ctx.Value(newPublicationKey{}).(*newPublicationWitness); witness != nil {
			return nil // The same physical writer checks that complete witness.
		}
	}
	// This is only the unowned fallback. A real protected producer takes the
	// closed witness path above without another copy of its private frame.
	var fields map[string]json.RawMessage
	if json.Unmarshal(request.Args, &fields) != nil {
		return namedSSHKeySourceError()
	}
	for _, field := range []string{"named_ssh_keys", "prepared"} {
		if raw := fields[field]; len(raw) != 0 && string(raw) != "null" {
			return namedSSHKeySourceError()
		}
	}
	return nil
}
