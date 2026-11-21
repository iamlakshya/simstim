package cli

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

func showNamedSSHKeys(session client.Session) string {
	if !session.Alive || !session.NamedSSHKeys.Valid() {
		return ""
	}
	return strings.Join(session.NamedSSHKeys.Installed, ", ")
}

func showNextNamedSSHKeys(session client.Session) string {
	if !session.Alive || !session.NamedSSHKeys.Valid() || slices.Equal(session.NamedSSHKeys.Installed, session.NamedSSHKeys.NextStart) {
		return ""
	}
	if len(session.NamedSSHKeys.NextStart) == 0 {
		return "none (at the next start)"
	}
	return strings.Join(session.NamedSSHKeys.NextStart, ", ") + " (at the next start)"
}
