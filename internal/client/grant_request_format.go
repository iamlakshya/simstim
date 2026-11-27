package client

import (
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// GrantRequest now owns protected role and arm capsules. Formatting the
// public request must not recursively inspect their unexported value caches.
func (*GrantRequest) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
