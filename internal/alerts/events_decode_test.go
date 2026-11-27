package alerts

import (
	"encoding/json"
	"testing"
)

func TestEventDecoderAndIdentityPreserveExactNativeSequence(t *testing.T) {
	event := Event{V: 1, Event: SandboxError, Session: Session{ID: "abcd2345"}, Time: "2026-11-15T10:00:00Z", Stream: "0123456789abcdef0123456789abcdef", Sequence: 9007199254740993}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStreamEvent(raw, "abcd2345")
	if err != nil || decoded.Sequence != event.Sequence {
		t.Fatalf("native sequence changed: %+v %v", decoded, err)
	}
	key, err := StreamEventIdentity(decoded)
	if err != nil || key != "0123456789abcdef0123456789abcdef:9007199254740993" {
		t.Fatalf("identity=%q err=%v", key, err)
	}
}

func TestEventDecoderRefusesUnredactedOrPartialReplayIdentity(t *testing.T) {
	for _, event := range []Event{
		{V: 1, Event: NeedsInput, Session: Session{ID: "abcd2345"}, Time: "2026-11-15T10:00:00Z", Detail: "private"},
		{V: 1, Event: NeedsInput, Session: Session{ID: "abcd2345"}, Time: "2026-11-15T10:00:00Z", Stream: "0123456789abcdef0123456789abcdef"},
	} {
		raw, _ := json.Marshal(event)
		if _, err := DecodeStreamEvent(raw, ""); err == nil {
			t.Fatalf("unverified stream record accepted: %s", raw)
		}
	}
}
