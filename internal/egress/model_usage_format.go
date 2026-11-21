package egress

import (
	"fmt"
	"io"
)

// Format keeps request labels and buffered model content out of diagnostics.
func (ModelUsageRequestV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<model usage request redacted>")
}

// Format never exposes a turn's private response buffers or model identity.
func (*ModelUsageTurnV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<model usage turn redacted>")
}

// Format keeps private accounting keys out of generic structured diagnostics.
func (*ModelUsageMeterV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<live model usage meter>")
}
