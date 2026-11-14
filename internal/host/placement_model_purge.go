package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

type purgeResource struct {
	Root       string       `json:"root"`
	Relative   []byte       `json:"relative"`
	Ref        purgeRef     `json:"ref"`
	Quarantine string       `json:"quarantine"`
	SHA256     string       `json:"sha256,omitempty"`
	Item       *supply.Item `json:"item,omitempty"`
	extra      map[string]json.RawMessage
}

type purgeSessionPlan struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Git             *GitInfo `json:"git,omitempty"`
	Run             Runtime  `json:"runtime"`
	Binary          string   `json:"holder_binary,omitempty"`
	Wall            []byte   `json:"wall,omitempty"`
	Directory       purgeRef `json:"directory"`
	Worktree        purgeRef `json:"worktree,omitempty"`
	WorktreeCapture []byte   `json:"worktree_capture,omitempty"`
	GitTip          string   `json:"git_tip,omitempty"`
	GitReleased     bool     `json:"git_released"`
	Stopped         bool     `json:"stopped"`
	Cleaned         bool     `json:"cleaned"`
	extra           map[string]json.RawMessage
}

type cleanupPayloadIntent struct {
	Scope     string                   `json:"scope"`
	Nonce     string                   `json:"operation_nonce"`
	Leaf      string                   `json:"leaf"`
	Directory cachefs.Reference        `json:"directory"`
	File      cachefs.Reference        `json:"file"`
	Owner     placementOperationRecord `json:"owner"`
	extra     map[string]json.RawMessage
}

type terminalCapture struct {
	Ref        purgeRef `json:"ref"`
	Quarantine string   `json:"quarantine"`
	SHA256     string   `json:"sha256,omitempty"`
	extra      map[string]json.RawMessage
}

func (intent *cleanupPayloadIntent) UnmarshalJSON(data []byte) error {
	type plain cleanupPayloadIntent
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"scope", "operation_nonce", "leaf", "directory", "file", "owner"})
	*intent = cleanupPayloadIntent(value)
	intent.extra = extra
	return err
}

func (intent cleanupPayloadIntent) MarshalJSON() ([]byte, error) {
	type plain cleanupPayloadIntent
	return encodeFootprint(plain(intent), intent.extra)
}

func (capture *terminalCapture) UnmarshalJSON(data []byte) error {
	type plain terminalCapture
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"ref", "quarantine", "sha256"})
	*capture = terminalCapture(value)
	capture.extra = extra
	return err
}

func (capture terminalCapture) MarshalJSON() ([]byte, error) {
	type plain terminalCapture
	return encodeFootprint(plain(capture), capture.extra)
}
