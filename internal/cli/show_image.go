package cli

import (
	"github.com/iamlakshya/simstim/internal/client"
)

func savedImageDetail(host, hostID string, session client.Session) []detail {
	if label := client.SavedImageLabel(session, host, hostID); label != "" {
		return []detail{{label: "Image", value: label}}
	}
	return nil
}
