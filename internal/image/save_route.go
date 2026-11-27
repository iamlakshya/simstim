package image

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// SaveArgsV1 routes a typed save to one original session. The values are
// ephemeral scanner input, never session or filesystem authority.
type SaveArgsV1 struct {
	Version int         `json:"v"`
	Session string      `json:"session"`
	Request SaveRequest `json:"request"`
}

func (SaveArgsV1) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }

// SaveSelectionArgsV1 keeps the session address separate from the pending
// save token, which is valid only on its original accepted connection.
type SaveSelectionArgsV1 struct {
	Version   int           `json:"v"`
	Session   string        `json:"session"`
	Selection SaveSelection `json:"selection"`
}
