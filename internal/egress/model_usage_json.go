package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

type modelUsageNumberV1 struct {
	value   uint64
	present bool
}

func (number *modelUsageNumberV1) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		*number = modelUsageNumberV1{}
		return nil
	}
	if len(raw) == 0 {
		return errors.New("empty usage count")
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return errors.New("model usage is not a nonnegative integer")
		}
	}
	value, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return fmt.Errorf("parse model usage count: %w", err)
	}
	*number = modelUsageNumberV1{value: value, present: true}
	return nil
}

type modelUsageFieldsV1 struct {
	Input        modelUsageNumberV1 `json:"input_tokens"`
	Output       modelUsageNumberV1 `json:"output_tokens"`
	CacheWrite   modelUsageNumberV1 `json:"cache_creation_input_tokens"`
	CacheRead    modelUsageNumberV1 `json:"cache_read_input_tokens"`
	Total        modelUsageNumberV1 `json:"total_tokens"`
	Prompt       modelUsageNumberV1 `json:"prompt_tokens"`
	Completion   modelUsageNumberV1 `json:"completion_tokens"`
	InputDetails struct {
		Cached modelUsageNumberV1 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		Reasoning modelUsageNumberV1 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	PromptDetails struct {
		Cached modelUsageNumberV1 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		Reasoning modelUsageNumberV1 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	ServerTools struct {
		Search modelUsageNumberV1 `json:"web_search_requests"`
	} `json:"server_tool_use"`
}

var modelUsageNamesV1 = []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "input_tokens_details", "output_tokens_details", "prompt_tokens_details", "completion_tokens_details", "server_tool_use"}

type modelUsageEventV1 struct {
	kind                                                      string
	status                                                    string
	usage                                                     modelUsageFieldsV1
	has                                                       bool
	badUsage                                                  bool
	responseID, eventID, requestEventID, streamID             [32]byte
	hasResponseID, hasEventID, hasRequestEventID, hasStreamID bool
}

// Standard JSON validation owns grammar. The shared borrowed projection then
// rejects duplicate/aliased known objects without materialising model content.
func parseModelUsageEventV1(raw []byte) (modelUsageEventV1, error) {
	return parseModelUsageEventContextV1(context.Background(), raw)
}

func parseModelUsageEventContextV1(ctx context.Context, raw []byte) (modelUsageEventV1, error) {
	if ctx == nil || ctx.Err() != nil {
		return modelUsageEventV1{}, errors.New("model usage source ended")
	}
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return modelUsageEventV1{}, errors.New("invalid model usage JSON")
	}
	projection, err := api.NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return modelUsageEventV1{}, err
	}
	span, err := projection.Root()
	if err != nil {
		return modelUsageEventV1{}, err
	}
	var event modelUsageEventV1
	err = projection.Object(span, []string{"type", "status", "usage", "message", "response", "response_id", "event_id", "error", "stream_id"}, func(field api.JSONField) error {
		switch field.Known {
		case 0, 1:
			value, err := readModelUsageKindV1(projection, field.Value)
			if err != nil {
				return fmt.Errorf("decode model usage kind: %w", err)
			}
			if field.Known == 0 {
				event.kind = value
			} else {
				event.status = value
			}
		case 2:
			usage, present, err := readModelUsageFieldsV1(raw, projection, field.Value)
			event.usage, event.has = usage, present
			return err
		case 3, 4:
			return projection.Object(field.Value, []string{"usage", "status", "id"}, func(nested api.JSONField) error {
				if nested.Known == 2 && field.Known == 4 {
					return readModelUsageIdentityV1(projection, nested.Value, &event.responseID, &event.hasResponseID)
				}
				if nested.Known == 0 {
					usage, present, err := readModelUsageFieldsV1(raw, projection, nested.Value)
					event.usage, event.has = usage, present
					return err
				}
				return nil
			})
		case 5:
			return readModelUsageIdentityV1(projection, field.Value, &event.responseID, &event.hasResponseID)
		case 6:
			return readModelUsageIdentityV1(projection, field.Value, &event.eventID, &event.hasEventID)
		case 8:
			return readModelUsageStreamV1(projection, field.Value, &event)
		case 7:
			if bytes.Equal(raw[field.Value.Start:field.Value.End], []byte("null")) {
				return nil
			}
			return projection.Object(field.Value, []string{"event_id"}, func(nested api.JSONField) error {
				if nested.Known == 0 {
					return readModelUsageIdentityV1(projection, nested.Value, &event.requestEventID, &event.hasRequestEventID)
				}
				return nil
			})
		}
		return nil
	})
	return event, err
}

func readModelUsageFieldsV1(raw []byte, projection *api.JSONProjection, span api.JSONSpan) (modelUsageFieldsV1, bool, error) {
	var usage modelUsageFieldsV1
	value := raw[span.Start:span.End]
	if bytes.Equal(value, []byte("null")) {
		return usage, false, nil
	}
	if err := projection.Object(span, modelUsageNamesV1, func(field api.JSONField) error {
		if field.Known >= 7 {
			if bytes.Equal(raw[field.Value.Start:field.Value.End], []byte("null")) {
				return nil
			}
			return projection.Object(field.Value, []string{"cached_tokens", "reasoning_tokens", "web_search_requests"}, func(api.JSONField) error { return nil })
		}
		return nil
	}); err != nil {
		return usage, false, err
	}
	if err := json.Unmarshal(value, &usage); err != nil {
		return usage, false, fmt.Errorf("decode model usage fields: %w", err)
	}
	return usage, true, nil
}
