package image

import (
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SaveLifetime bounds the review of an unkept converted root.
const SaveLifetime = time.Hour

// SaveRequest contains only the laptop's choices and ephemeral scan values.
// It grants no filesystem, session, host or publication authority.
type SaveRequest struct {
	Ref     string                `json:"ref"`
	Values  map[string]api.Secret `json:"values"`
	Replace bool                  `json:"replace,omitempty"`
}

// Format prevents accidental logging of a request's private scanner input.
func (SaveRequest) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }

// SaveSelection names a pending save on its original accepted connection.
type SaveSelection struct {
	ID string `json:"id"`
}

// SaveFinding describes a path without including file or secret content.
type SaveFinding struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SaveReview is the complete review required before publication.
type SaveReview struct {
	ID       string        `json:"id"`
	Ref      string        `json:"ref"`
	Size     int64         `json:"size"`
	Flagged  []SaveFinding `json:"flagged"`
	Excluded []SaveFinding `json:"excluded"`
}

// SaveResult is valid only on the original host. Its two digests identify
// rootfs.img itself: a saved image has no OCI manifest or signature.
type SaveResult struct {
	Image  Record `json:"image"`
	HostID string `json:"host_id"`
}

var savedReference = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidSaveRef checks the deliberately local saved-image namespace.
func ValidSaveRef(ref string) bool { return savedReference.MatchString(ref) }

func saveFailure(code, message string) error {
	return &proto.Error{Code: code, Message: message, Hint: "Inspect the session with shell, then save its image again."}
}
