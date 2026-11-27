package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type rewindPublicationKeyV1 struct{}

// Every rewind frame, including value-free prepare/check and cleanup, must
// come from its real private producer on the original accepted connection.
func beforeRewindRequestV1(ctx context.Context, c *Conn, request proto.Request, arguments any) error {
	p := originalRewindV1(ctx)
	if p != nil && (request.Op == "resume" || request.Op == "resume.prepare-v1" || request.Op == "resume.prepared.check-v1" || request.Op == "resume.prepared.cancel-v1") {
		return rewindSourceErrorV1()
	}
	if request.Op == api.OpRewind {
		return rewindSourceErrorV1()
	}
	switch request.Op {
	case api.OpRewindPrepareV1, api.OpRewindPreparedCheckV1, api.OpRewindCommitV1, api.OpRewindPreparedCancelV1:
	default:
		return nil
	}
	selected, _ := ctx.Value(rewindPublicationKeyV1{}).(*RewindPreparation)
	if p == nil || selected != p || p.conn != c || p.stamp != c.hostTransport {
		return rewindSourceErrorV1()
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || !bytes.Equal(encoded, request.Args) {
		return rewindSourceErrorV1()
	}
	if request.Op == api.OpRewindPreparedCancelV1 {
		args, ok := arguments.(api.RewindPreparedArgsV1)
		if !ok || args.Validate() != nil {
			return rewindSourceErrorV1()
		}
		p.mu.Lock()
		valid := p.closing && p.prepared != nil && reflect.DeepEqual(args.Prepared, p.prepared.Prepared) && args.Selection == p.selection
		p.mu.Unlock()
		if !valid {
			return rewindSourceErrorV1()
		}
		return c.CheckSavedSource(ctx)
	}
	if err := p.checkLocal(ctx); err != nil {
		return err
	}
	switch request.Op {
	case api.OpRewindPrepareV1:
		args, ok := arguments.(api.RewindPrepareArgsV1)
		p.mu.Lock()
		valid := !p.attempted && p.prepared == nil && bytes.Equal(encoded, p.metadata)
		p.mu.Unlock()
		if !ok || args.Validate() != nil || !valid || args.Selection != p.selection || args.Floor != p.floor {
			return rewindSourceErrorV1()
		}
	case api.OpRewindPreparedCheckV1:
		args, ok := arguments.(api.RewindPreparedArgsV1)
		p.mu.Lock()
		valid := p.prepared != nil && reflect.DeepEqual(args.Prepared, p.prepared.Prepared)
		p.mu.Unlock()
		if !ok || args.Validate() != nil || !valid || args.Selection != p.selection {
			return rewindSourceErrorV1()
		}
	case api.OpRewindCommitV1:
		args, ok := arguments.(api.RewindCommitArgsV1)
		if !ok || args.Validate() != nil || args.Selection != p.selection {
			return rewindSourceErrorV1()
		}
		if err := p.checkStartMetadataV1(args.Start); err != nil {
			return err
		}
		p.mu.Lock()
		witness := p.publication
		p.mu.Unlock()
		if witness == nil {
			return sandboxReceiptError()
		}
		if err := witness.beforeRequestV3(ctx, c, request); err != nil {
			return err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.commitAttempted || p.committed || !reflect.DeepEqual(args.Prepared, p.prepared.Prepared) || len(args.Start.Members) != len(p.group.snapshot.Members) || !sameSelectedSecretValues(args.Start.Secrets, p.values[p.group.snapshot.Primary]) {
			return rewindSourceErrorV1()
		}
		for index, id := range p.group.snapshot.Members {
			member := args.Start.Members[index]
			if member.ID != id || !sameSelectedSecretValues(member.Secrets, p.values[id]) || len(member.ProtectedTrustedSecretValues) != 0 || member.SecretDelivery != nil && len(member.SecretDelivery.TrustedUses) != 0 {
				return rewindSourceErrorV1()
			}
		}
		if !slices.Equal(secretMapNames(args.Start.Secrets), p.group.primaryNames) {
			return rewindSourceErrorV1()
		}
	}
	return nil
}

// The ordered writer marks an attempt only after all original image, named
// key, judge and H5 guards pass. A prompt before publication remains retryable.
func markRewindPublicationV1(ctx context.Context, c *Conn, request proto.Request) error {
	if request.Op != api.OpRewindPrepareV1 && request.Op != api.OpRewindCommitV1 {
		return nil
	}
	p, _ := ctx.Value(rewindPublicationKeyV1{}).(*RewindPreparation)
	if p == nil || p != originalRewindV1(ctx) || p.conn != c {
		return rewindSourceErrorV1()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.Op == api.OpRewindPrepareV1 {
		if p.attempted {
			return rewindSourceErrorV1()
		}
		p.attempted = true
	} else {
		if p.commitAttempted {
			return rewindSourceErrorV1()
		}
		p.commitAttempted = true
	}
	return nil
}
