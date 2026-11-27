package client

import (
	"github.com/iamlakshya/simstim/internal/image"
)

// SavedImageLabel is display text for a live image origin on the current
// accepted host. Callers supply that connection's identity and saved alias.
// It supplies no image-selection, store or session authority.
func SavedImageLabel(session Session, host, hostID string) string {
	if !session.Alive || session.Wall == nil || session.Wall.Level != "microvm" || session.Wall.SavedImage == nil || !ValidHostID(hostID) || CleanLine(host) == "" {
		return ""
	}
	origin := session.Wall.SavedImage
	identity := image.SavedHostV1{Version: origin.Version, HostID: origin.HostID, Ref: origin.Ref, Digest: origin.Digest}
	if identity.Validate() != nil || identity.HostID != hostID {
		return ""
	}
	return identity.Ref + " (made on " + CleanLine(host) + ")"
}
