package egress

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

const CtlSecretSaved = "secret.saved"

// PublicMintedSecretState contains only a public response stand-in. After a
// cold restore it is permanently unarmed and needs a fresh origin mint. No
// local command or primary-name arm can recover its real server-issued value.
type PublicMintedSecretState = api.PublicMintedSecretState

// MintedStateEvent reserves one durable public record on the existing trusted
// control peer. The receiver saves it before acknowledging the exact receipt.
type MintedStateEvent struct {
	Version    int                     `json:"version"`
	Generation string                  `json:"generation"`
	Receipt    string                  `json:"receipt"`
	Owner      string                  `json:"owner"`
	State      PublicMintedSecretState `json:"state"`
}

type SecretSavedArgs struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
	Receipt    string `json:"receipt"`
	Owner      string `json:"owner"`
	Identity   string `json:"identity"`
	Durable    bool   `json:"durable"`
}

func (MintedStateEvent) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (SecretSavedArgs) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }
