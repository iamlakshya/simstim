package classify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type modelCall func(context.Context, []byte) ([]byte, error)

func (call modelCall) Complete(ctx context.Context, raw []byte) ([]byte, error) {
	return call(ctx, raw)
}

func TestModelAdaptersSendQuotedInputAndAcceptOnlyCompletedAnswers(t *testing.T) {
	for _, protocol := range []ModelProtocol{OpenAIChat, AnthropicMessage} {
		t.Run(string(protocol), func(t *testing.T) {
			called := false
			caller := modelCall(func(_ context.Context, raw []byte) ([]byte, error) {
				called = true
				var request struct {
					Model    string         `json:"model"`
					Messages []modelMessage `json:"messages"`
					System   string         `json:"system"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Fatal(err)
				}
				if request.Model != "fixture-model" {
					t.Fatal("model selection changed")
				}
				last := request.Messages[len(request.Messages)-1]
				var input Input
				if last.Role != "user" || json.Unmarshal([]byte(last.Content), &input) != nil || input.Host != testInput().Host {
					t.Fatal("input became unquoted instructions")
				}
				if protocol == OpenAIChat {
					return []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"action\":\"allow\",\"reason\":\"docs\",\"scope\":\"session\"}"}}]}`), nil
				}
				return []byte(`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"{\"action\":\"allow\",\"reason\":\"docs\",\"scope\":\"session\"}"}]}`), nil
			})
			judge, err := NewModel("fixture-model", protocol, "once", caller)
			if err != nil {
				t.Fatal(err)
			}
			result, err := judge.Decide(t.Context(), testInput())
			if err != nil || !called || result.Action != "allow" || result.Scope != "once" {
				t.Fatalf("adapter failed: %+v %v", result, err)
			}
		})
	}
}

func TestModelAdaptersRefuseTruncationToolsMultipleChoicesAndOversizedEnvelope(t *testing.T) {
	valid := `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"action\":\"allow\",\"reason\":\"docs\",\"scope\":\"session\"}"}}]}`
	for _, raw := range []string{
		strings.Replace(valid, `"stop"`, `"length"`, 1),
		strings.Replace(valid, `"role":"assistant"`, `"role":"assistant","tool_calls":[{}]`, 1),
		strings.Replace(valid, `"role":"assistant"`, `"role":"assistant","refusal":"refused"`, 1),
		strings.Replace(valid, `"choices":`, `"Choices":`, 1),
		`{"choices":[],"choices":[{}]}`, `{"choices":[{},{}]}`,
		strings.Repeat(" ", MaxModelAnswerBytes+1),
	} {
		judge, err := NewModel("fixture-model", OpenAIChat, "session", modelCall(func(context.Context, []byte) ([]byte, error) { return []byte(raw), nil }))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := judge.Decide(t.Context(), testInput()); err == nil {
			t.Fatal("malformed API result became a decision")
		}
	}
}

func TestCancelledJudgeNeverCallsModel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	judge, err := NewModel("fixture-model", OpenAIChat, "session", modelCall(func(context.Context, []byte) ([]byte, error) {
		t.Fatal("cancelled judge called model")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := judge.Decide(ctx, testInput()); err == nil {
		t.Fatal("cancelled model succeeded")
	}
}

func TestModelCompatibleEmptyToolAndRefusalFieldsStillReturnACompleteAnswer(t *testing.T) {
	judge, err := NewModel("fixture-model", OpenAIChat, "session", modelCall(func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","refusal":"","tool_calls":[],"content":"{\"action\":\"allow\",\"reason\":\"docs\",\"scope\":\"session\"}"}}]}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := judge.Decide(t.Context(), testInput())
	if err != nil || actual.Action != "allow" {
		t.Fatal("empty compatible response markers became an unsupported judge", err)
	}
}
