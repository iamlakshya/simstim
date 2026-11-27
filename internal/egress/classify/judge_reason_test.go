package classify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestConfiguredJudgeRequiresReasonSourceBeforeModelIO(t *testing.T) {
	kind, model := "model", "configured-model"
	calls := 0
	judge, err := NewConfigured(api.ClassifierValues{Judge: &kind, Model: &model}, OpenAIChat,
		modelCall(func(context.Context, []byte) ([]byte, error) { calls++; return nil, errors.New("unexpected call") }))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := judge.Decide(t.Context(), testInput()); err == nil || result != (Result{}) || calls != 0 {
		t.Fatal("configured adapter bypassed its mandatory source hook")
	}
	kind, program := "command", "/usr/bin/false"
	judge, err = NewConfigured(api.ClassifierValues{Judge: &kind, Command: &program}, "", nil)
	if err == nil {
		if result, err := judge.Decide(t.Context(), testInput()); err == nil || result != (Result{}) {
			t.Fatal("configured command bypassed its mandatory source hook")
		}
	}
}

func TestJudgeReasonSourceSeesFullDecodedReasonBeforeTransform(t *testing.T) {
	for _, length := range []int{256, 1021} {
		secret := strings.Repeat("AbC", length/3) + strings.Repeat("z", length%3)
		rawReason := "\x1b" + secret + "\n" + strings.Repeat("é", 220)
		raw, err := json.Marshal(Result{Action: "allow", Reason: rawReason, Scope: "session"})
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		ctx := WithReasonSanitizer(t.Context(), func(_ context.Context, reason string) (string, error) {
			calls++
			if reason != rawReason {
				t.Fatal("codec changed the full raw reason before source sanitisation")
			}
			return strings.ReplaceAll(reason, secret, "[CREDENTIAL]"), nil
		})
		result, err := decodeResult(ctx, raw, "once")
		if err != nil || calls != 1 || result.Scope != "once" || !strings.HasPrefix(result.Reason, "[CREDENTIAL]") || strings.Contains(result.Reason, secret[:200]) || strings.ContainsAny(result.Reason, "\x1b\n") || utf8.RuneCountInString(result.Reason) != 200 {
			t.Fatal("whole source scrub did not precede the public reason limit", err)
		}
	}
}

func TestJudgeMalformedAndStaleReasonPublishesNoPartialText(t *testing.T) {
	calls := 0
	ctx := WithReasonSanitizer(t.Context(), func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("source replaced")
	})
	for _, raw := range []string{
		`{"action":"allow","reason":"partial-secret`,
		`{"action":"allow","reason":null,"scope":"once"}`,
		`{"action":"allow","scope":"once"}`,
	} {
		if result, err := decodeResult(ctx, []byte(raw), "once"); err == nil || result != (Result{}) {
			t.Fatal("malformed output exposed a partial reason")
		}
	}
	if calls != 0 {
		t.Fatal("incomplete JSON reached the source hook")
	}
	if result, err := decodeResult(ctx, []byte(`{"action":"allow","reason":"complete-secret","scope":"once"}`), "once"); err == nil || result != (Result{}) || calls != 1 {
		t.Fatal("stale source exposed a partial reason")
	}
}

func TestJudgeInvalidUTF8CannotPublishAPartialReason(t *testing.T) {
	calls := 0
	ctx := WithReasonSanitizer(t.Context(), func(context.Context, string) (string, error) { calls++; return "sanitised", nil })
	raw := append([]byte(`{"action":"allow","reason":"`+strings.Repeat("A", 256)), 0xff)
	raw = append(raw, []byte(`","scope":"once"}`)...)
	if result, err := decodeResult(ctx, raw, "once"); err == nil || result != (Result{}) || calls != 0 {
		t.Fatal("invalid UTF-8 reached a partial reason or its source hook")
	}
	chat := append([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}],"extra":"`), 0xff)
	chat = append(chat, []byte(`"}`)...)
	if _, err := chatContent(chat); err == nil {
		t.Fatal("invalid model envelope UTF-8 was silently replaced")
	}
	message := append([]byte(`{"role":"assistant","type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"{}"}],"extra":"`), 0xff)
	message = append(message, []byte(`"}`)...)
	if _, err := messageContent(message); err == nil {
		t.Fatal("invalid message envelope UTF-8 was silently replaced")
	}
}
