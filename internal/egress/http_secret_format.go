package egress

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

func (boundHTTPSecrets) Format(f fmt.State, _ rune)        { _, _ = io.WriteString(f, api.Redacted) }
func (*boundHTTPSecretState) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, api.Redacted) }
func (secretOriginInput) Format(f fmt.State, _ rune)       { _, _ = io.WriteString(f, api.Redacted) }
func (*secretOriginInputState) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (secretResponseBody) Format(f fmt.State, _ rune)      { _, _ = io.WriteString(f, api.Redacted) }
func (*secretResponseBodyState) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, api.Redacted)
}
func (secretRequestTrailerBody) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, api.Redacted)
}
func (secretWebSocketClient) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
