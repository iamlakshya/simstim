package supervisor

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeatureNativeResumeV1) }

// nativeWarmResumeRequestV1 consumes only the same actual inspected private
// controller scope. The agent-only backend cannot stop/rebuild its VM or
// install a new named-key set when this original scope changes or fails.
func (s *Supervisor) nativeWarmResumeRequestV1(client *client, request proto.Request) bool {
	if request.Op != proto.NativeWarmResumeV1 {
		return false
	}
	if !client.full {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the warm resume has no original full native peer")))
		return true
	}
	wire, args, err := decodeNativeWarmResumeV1(request.Args)
	if err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	scope := client.nativeScopeV1()
	if scope == nil || scope.used || scope.purpose != proto.NativeResumeInspectV1 || wire.Scope.Primary != scope.state.Primary || wire.Scope.Nonce != scope.state.Nonce {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the original warm resume scope was consumed or changed")))
		return true
	}
	ctx := scope.root.lifetimeContext()
	if err := scope.checkCurrentV1(ctx); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	// Use the existing tracked request lifetime, whose shutdown cancels and
	// joins each worker before the original runtime resources can close.
	ctx, finish, err := scope.root.beginFileTransfer(client)
	if err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	ctx, cancel := context.WithCancel(ctx)
	scope.used = true
	scope.mu.Lock()
	scope.warmCancel = cancel
	scope.warmDone = make(chan struct{})
	done := scope.warmDone
	scope.mu.Unlock()
	go func() {
		select {
		case <-client.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		var result *api.ResumeResult
		err := scope.checkCurrentV1(ctx)
		if err == nil {
			result, err = resumeAgentForNativeScopeV1(ctx, scope, args)
		}
		if err == nil {
			err = scope.checkCurrentV1(scope.lifetime)
		}
		cancel()
		finish()
		scope.mu.Lock()
		if err != nil && scope.warmMutation {
			scope.warmUncertain = err
		}
		scope.mu.Unlock()
		close(done) // Reply is the explicit completion ACK after owned cleanup.
		if err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
		} else {
			client.reply(request.ID, result, nil)
		}
	}()
	return true
}

// Decode the closed wrapper separately: Request is deliberately raw so its
// own sole DTO and canonical image/secret decoders can validate it once.
func decodeNativeWarmResumeV1(raw json.RawMessage) (proto.NativeWarmResumeArgsV1, api.ResumeArgs, error) {
	var wire proto.NativeWarmResumeArgsV1
	var args api.ResumeArgs
	fields, err := api.JSONObjectFields(raw, "v", "scope", "request")
	if err != nil || len(fields) != 3 || !api.ExactJSONFields(fields["scope"], proto.NativeStopArgsV1{}) || json.Unmarshal(raw, &wire) != nil || wire.Version != 1 || wire.Scope.Version != 1 {
		return wire, args, execRefusal("the warm resume request has no exact original native scope")
	}
	selected, err := api.JSONObjectFields(wire.Request)
	if err != nil {
		return wire, args, execRefusal("the warm resume declaration is malformed")
	}
	if keys, exists := selected["named_ssh_keys"]; exists && !bytes.Equal(keys, []byte("null")) {
		return wire, args, execRefusal("the warm controller cannot receive a new named-key set")
	}
	if !api.ExactJSONFields(wire.Request, args) || json.Unmarshal(wire.Request, &args) != nil {
		return wire, args, execRefusal("the warm resume declaration is malformed")
	}
	return wire, args, nil
}
