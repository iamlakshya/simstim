package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// The borrowed canonical JSON projection hashes every decoded rune. Long IDs
// retain their complete identity without a second grammar or a length cap.
func readModelUsageIdentityV1(projection *api.JSONProjection, span api.JSONSpan, target *[32]byte, present *bool) error {
	hash := sha256.New()
	var buffer [4096 + utf8.UTFMax]byte
	used := 0
	if err := projection.WalkString(span, func(value rune) error {
		if len(buffer)-used < utf8.UTFMax {
			_, _ = hash.Write(buffer[:used])
			used = 0
		}
		used = len(utf8.AppendRune(buffer[:used], value))
		return nil
	}); err != nil {
		return errors.New("invalid model response identity")
	}
	if used > 0 {
		_, _ = hash.Write(buffer[:used])
	}
	clear(buffer[:])
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	if *present && *target != sum {
		return errors.New("conflicting model response identity")
	}
	*target, *present = sum, true
	return nil
}

// Identity recovery uses the SAME borrowed projection and standard JSON
// grammar when a provider's usage counters are malformed. It cannot turn
// those counters into reported usage or guess an unknown response ID.
func modelUsageWebSocketIdentityV1(ctx context.Context, raw []byte) (modelUsageEventV1, error) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return modelUsageEventV1{}, errors.New("invalid model event")
	}
	p, err := api.NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return modelUsageEventV1{}, err
	}
	root, err := p.Root()
	if err != nil {
		return modelUsageEventV1{}, err
	}
	var event modelUsageEventV1
	err = p.Object(root, []string{"type", "response_id", "event_id", "response", "error", "stream_id"}, func(field api.JSONField) error {
		switch field.Known {
		case 0:
			kind, err := readModelUsageKindV1(p, field.Value)
			event.kind = kind
			return err
		case 1:
			return readModelUsageIdentityV1(p, field.Value, &event.responseID, &event.hasResponseID)
		case 2:
			return readModelUsageIdentityV1(p, field.Value, &event.eventID, &event.hasEventID)
		case 3:
			return p.Object(field.Value, []string{"id"}, func(nested api.JSONField) error {
				if nested.Known == 0 {
					return readModelUsageIdentityV1(p, nested.Value, &event.responseID, &event.hasResponseID)
				}
				return nil
			})
		case 5:
			return readModelUsageStreamV1(p, field.Value, &event)
		case 4:
			if bytes.Equal(raw[field.Value.Start:field.Value.End], []byte("null")) {
				return nil
			}
			return p.Object(field.Value, []string{"event_id"}, func(nested api.JSONField) error {
				if nested.Known == 0 {
					return readModelUsageIdentityV1(p, nested.Value, &event.requestEventID, &event.hasRequestEventID)
				}
				return nil
			})
		}
		return nil
	})
	return event, err
}

// Event kinds are finite protocol selectors. Walk the complete decoded string
// through the existing projection; JSON escape spelling imposes no text cap.
// An unknown long value remains unknown without retaining arbitrary input.
func readModelUsageKindV1(p *api.JSONProjection, span api.JSONSpan) (string, error) {
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return "", err
	}
	var decoded [32]byte
	used, overflow := 0, false
	err = p.WalkString(span, func(value rune) error {
		if overflow {
			return nil
		}
		if utf8.RuneLen(value) > len(decoded)-used {
			overflow = true
			return nil
		}
		used = len(utf8.AppendRune(decoded[:used], value))
		return nil
	})
	if err != nil {
		return "", err
	}
	if overflow {
		return "\x00", nil // finite nonempty selector, matching no known event/status
	}
	return string(decoded[:used]), nil
}

func readModelUsageStreamV1(p *api.JSONProjection, span api.JSONSpan, event *modelUsageEventV1) error {
	null, err := p.Literal(span, "null")
	if err != nil || null {
		return err
	}
	return readModelUsageIdentityV1(p, span, &event.streamID, &event.hasStreamID)
}
