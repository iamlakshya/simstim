package egress

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const privateSizedID = "00000000000000000000000000000000"

type privateFrameCounter struct{ bytes int }

func (c *privateFrameCounter) Write(data []byte) (int, error) {
	c.bytes += len(data)
	return len(data), nil
}

func privateGrantCapacity() error {
	return memberError("state_too_large", "private session allowances exceed the existing complete state limit", "Remove an unused session allowance or start a fresh sandbox with only required rules and secrets.")
}

// Count exactly the existing full state frame using empty public placeholders.
// The true canonical ASCII target is NEVER assigned to api.Rule or encoded.
func checkMemoryGrantSize(ctx context.Context, state *memberState, engine *Engine) error {
	surrogate := *state
	surrogate.metadata = state.metadata
	policy := state.metadata.Policy
	policy.Rules = slices.Clone(policy.Rules)
	targetBytes := 0
	found := false
	for _, layer := range engine.layers {
		for _, row := range layer.rules {
			if !row.memoryOnly {
				continue
			}
			found = true
			target := formatCompiledTarget(row)
			for i := range len(target) {
				if target[i] < 0x20 || target[i] > 0x7e || target[i] == '"' || target[i] == '\\' || target[i] == '<' || target[i] == '>' || target[i] == '&' {
					return privateGrantCapacity()
				}
			}
			if len(target) > proto.MaxFrame-targetBytes {
				return privateGrantCapacity()
			}
			targetBytes += len(target)
			// Private-only origin/display metadata also spends the same byte cap.
			metadataBytes := len(`,"memory_only":true,"display_sensitive":false,"by":""`) + len(row.by)
			if row.uploadBudget {
				metadataBytes += len(`,"upload_budget":true`)
			}
			if row.displaySensitive {
				metadataBytes--
			}
			if metadataBytes > proto.MaxFrame-targetBytes {
				return privateGrantCapacity()
			}
			targetBytes += metadataBytes
			policy.Rules = append(policy.Rules, api.Rule{ID: privateSizedID, GrantID: privateSizedID, Layer: api.LayerSession, Allow: true, Door: api.DoorNet, PublicOnly: row.publicOnly, Once: row.once, Until: row.until})
		}
	}
	if !found {
		return nil
	}
	surrogate.metadata.Policy = policy
	var count int
	var err error
	if surrogate.version == MemberRegistryV2 {
		count, err = boundStateFrameBytes(ctx, &surrogate)
	} else {
		var args InitArgs
		args, err = surrogate.initialArgs()
		if err == nil {
			var raw []byte
			raw, err = json.Marshal(args)
			if err == nil {
				counter := new(privateFrameCounter)
				err = proto.NewWriter(counter).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlInit, Args: raw})
				count = counter.bytes - 4
			}
		}
	}
	if errors.Is(err, proto.ErrFrameTooBig) {
		return privateGrantCapacity()
	}
	if err != nil {
		return err
	}
	if count < 0 || count > proto.MaxFrame-targetBytes {
		return privateGrantCapacity()
	}
	return nil
}
