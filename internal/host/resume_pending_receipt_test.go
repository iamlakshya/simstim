package host

import (
	"encoding/json"
	"testing"
)

func TestPendingCancellationReceiptMatchesTheCanonicalProducer(t *testing.T) {
	raw, err := json.Marshal(map[string]bool{"killed": true})
	if err != nil || !validPendingCancellation(raw) {
		t.Fatalf("canonical pending producer receipt = %s, %v", raw, err)
	}
	for _, invalid := range []string{
		`{}`, `null`, `{"killed":false}`, `{"Killed":true}`,
		`{"killed":true,"killed":true}`, `{"killed":true,"future":false}`,
		`{"killed":true} {}`, `{"killed":true`, ` {"killed":true}`,
	} {
		if validPendingCancellation([]byte(invalid)) {
			t.Fatalf("noncanonical pending cancellation receipt accepted: %s", invalid)
		}
	}
}
